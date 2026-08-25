package xrayrt

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

const (
	generationConfigMagic       = "XTRTGEN\x00"
	generationConfigVersion     = uint16(1)
	generationConfigHeaderSize  = len(generationConfigMagic) + 2 + 4 + sha256.Size
	maxGenerationConfigBodySize = 16 << 20
	maxGenerationHandlers       = 1024
	maxGenerationTagLength      = 128
	maxTypedMessageDepth        = 64
	generationTagPrefix         = "x-tier/g/"
)

var ErrInvalidGenerationConfig = errors.New("xrayrt: invalid generation config")

// GenerationConfig is an immutable, integrity-checked outbound generation.
// Its JSON form intentionally exposes only verification metadata; Xray's
// internal configuration tree never becomes part of the CLI/API schema.
type GenerationConfig struct {
	encoded []byte
}

// GenerationConfigInfo is safe to expose through status and CLI surfaces.
type GenerationConfigInfo struct {
	Schema       string `json:"schema"`
	SHA256       string `json:"sha256"`
	EncodedBytes int    `json:"encoded_bytes"`
}

// NewGenerationConfig validates and canonically encodes outbound handlers.
// The input is cloned and is never retained or modified.
func NewGenerationConfig(outbounds []*core.OutboundHandlerConfig) (GenerationConfig, error) {
	config := &core.Config{Outbound: cloneOutboundConfigs(outbounds)}
	body, err := canonicalGenerationBody(config)
	if err != nil {
		return GenerationConfig{}, err
	}
	return generationConfigFromBody(body), nil
}

// ParseGenerationConfig verifies framing, digest, schema, and canonical
// protobuf encoding before producing an opaque GenerationConfig value.
func ParseGenerationConfig(encoded []byte) (GenerationConfig, error) {
	body, err := verifyGenerationEnvelope(encoded)
	if err != nil {
		return GenerationConfig{}, err
	}
	config := new(core.Config)
	if err := (proto.UnmarshalOptions{RecursionLimit: maxTypedMessageDepth}).Unmarshal(body, config); err != nil {
		return GenerationConfig{}, invalidGenerationConfig("decode payload", err)
	}
	canonical, err := canonicalGenerationBody(config)
	if err != nil {
		return GenerationConfig{}, err
	}
	if !bytes.Equal(body, canonical) {
		return GenerationConfig{}, invalidGenerationConfig("payload is not canonical", nil)
	}
	return GenerationConfig{encoded: bytes.Clone(encoded)}, nil
}

func (c GenerationConfig) MarshalBinary() ([]byte, error) {
	if _, err := verifyGenerationEnvelope(c.encoded); err != nil {
		return nil, err
	}
	return bytes.Clone(c.encoded), nil
}

func (c *GenerationConfig) UnmarshalBinary(encoded []byte) error {
	if c == nil {
		return invalidGenerationConfig("nil destination", nil)
	}
	parsed, err := ParseGenerationConfig(encoded)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

func (c GenerationConfig) Info() (GenerationConfigInfo, error) {
	body, err := verifyGenerationEnvelope(c.encoded)
	if err != nil {
		return GenerationConfigInfo{}, err
	}
	digest := sha256.Sum256(body)
	return GenerationConfigInfo{
		Schema:       fmt.Sprintf("xray-outbound-generation/v%d", generationConfigVersion),
		SHA256:       hex.EncodeToString(digest[:]),
		EncodedBytes: len(c.encoded),
	}, nil
}

func (c GenerationConfig) MarshalJSON() ([]byte, error) {
	info, err := c.Info()
	if err != nil {
		return nil, err
	}
	return json.Marshal(info)
}

func (*GenerationConfig) UnmarshalJSON([]byte) error {
	return invalidGenerationConfig("JSON metadata cannot be used as runtime configuration", nil)
}

func (c GenerationConfig) String() string {
	info, err := c.Info()
	if err != nil {
		return "xray-outbound-generation:invalid"
	}
	return info.Schema + ":sha256:" + info.SHA256
}

func (c GenerationConfig) clone() GenerationConfig {
	return GenerationConfig{encoded: bytes.Clone(c.encoded)}
}

func (c GenerationConfig) decode() (*core.Config, error) {
	parsed, err := ParseGenerationConfig(c.encoded)
	if err != nil {
		return nil, err
	}
	body, err := verifyGenerationEnvelope(parsed.encoded)
	if err != nil {
		return nil, err
	}
	config := new(core.Config)
	if err := proto.Unmarshal(body, config); err != nil {
		return nil, invalidGenerationConfig("decode verified payload", err)
	}
	return config, nil
}

func generationConfigFromBody(body []byte) GenerationConfig {
	digest := sha256.Sum256(body)
	encoded := make([]byte, generationConfigHeaderSize+len(body))
	copy(encoded, generationConfigMagic)
	binary.BigEndian.PutUint16(encoded[len(generationConfigMagic):], generationConfigVersion)
	binary.BigEndian.PutUint32(encoded[len(generationConfigMagic)+2:], uint32(len(body)))
	copy(encoded[len(generationConfigMagic)+2+4:], digest[:])
	copy(encoded[generationConfigHeaderSize:], body)
	return GenerationConfig{encoded: encoded}
}

func verifyGenerationEnvelope(encoded []byte) ([]byte, error) {
	if len(encoded) < generationConfigHeaderSize {
		return nil, invalidGenerationConfig("truncated envelope", nil)
	}
	if string(encoded[:len(generationConfigMagic)]) != generationConfigMagic {
		return nil, invalidGenerationConfig("bad magic", nil)
	}
	versionOffset := len(generationConfigMagic)
	if version := binary.BigEndian.Uint16(encoded[versionOffset:]); version != generationConfigVersion {
		return nil, invalidGenerationConfig(fmt.Sprintf("unsupported version %d", version), nil)
	}
	lengthOffset := versionOffset + 2
	bodyLength := uint64(binary.BigEndian.Uint32(encoded[lengthOffset:]))
	if bodyLength == 0 || bodyLength > maxGenerationConfigBodySize {
		return nil, invalidGenerationConfig(fmt.Sprintf("payload length %d is out of range", bodyLength), nil)
	}
	if bodyLength != uint64(len(encoded)-generationConfigHeaderSize) {
		return nil, invalidGenerationConfig("payload length mismatch", nil)
	}
	body := encoded[generationConfigHeaderSize:]
	wantDigest := encoded[lengthOffset+4 : generationConfigHeaderSize]
	digest := sha256.Sum256(body)
	if !bytes.Equal(wantDigest, digest[:]) {
		return nil, invalidGenerationConfig("payload digest mismatch", nil)
	}
	return body, nil
}

func canonicalGenerationBody(config *core.Config) ([]byte, error) {
	if config == nil {
		return nil, invalidGenerationConfig("nil payload", nil)
	}
	if len(config.Inbound) != 0 || len(config.App) != 0 || len(config.Extension) != 0 {
		return nil, invalidGenerationConfig("only outbound handlers are permitted", nil)
	}
	if len(config.Outbound) == 0 || len(config.Outbound) > maxGenerationHandlers {
		return nil, invalidGenerationConfig(fmt.Sprintf("handler count %d is out of range", len(config.Outbound)), nil)
	}
	if err := canonicalizeMessage(config.ProtoReflect(), "generation", 0); err != nil {
		return nil, err
	}

	tags := make(map[string]struct{}, len(config.Outbound))
	for index, outbound := range config.Outbound {
		path := fmt.Sprintf("outbound[%d]", index)
		if outbound == nil {
			return nil, invalidGenerationConfig(path+" is nil", nil)
		}
		if err := validateGenerationTag(outbound.Tag); err != nil {
			return nil, invalidGenerationConfig(path+" tag", err)
		}
		if _, exists := tags[outbound.Tag]; exists {
			return nil, invalidGenerationConfig(fmt.Sprintf("duplicate outbound tag %q", outbound.Tag), nil)
		}
		tags[outbound.Tag] = struct{}{}
		if outbound.ProxySettings == nil {
			return nil, invalidGenerationConfig(path+" has no proxy settings", nil)
		}
		if outbound.Expire != 0 || outbound.Comment != "" {
			return nil, invalidGenerationConfig(path+" contains unsupported metadata", nil)
		}
		if _, err := typedMessageInstance(outbound.ProxySettings, path+".proxy_settings"); err != nil {
			return nil, err
		}
	}

	for index, outbound := range config.Outbound {
		if outbound.SenderSettings == nil {
			continue
		}
		path := fmt.Sprintf("outbound[%d].sender_settings", index)
		message, err := typedMessageInstance(outbound.SenderSettings, path)
		if err != nil {
			return nil, err
		}
		sender, ok := message.(*proxyman.SenderConfig)
		if !ok {
			return nil, invalidGenerationConfig(path+" is not a proxyman.SenderConfig", nil)
		}
		if sender.ProxySettings != nil {
			if err := validateGenerationReference(outbound.Tag, "proxy_settings.tag", sender.ProxySettings.Tag, tags); err != nil {
				return nil, err
			}
		}
		if sender.StreamSettings != nil && sender.StreamSettings.SocketSettings != nil {
			if err := validateGenerationReference(outbound.Tag, "socket_settings.dialer_proxy", sender.StreamSettings.SocketSettings.DialerProxy, tags); err != nil {
				return nil, err
			}
		}
	}

	body, err := (proto.MarshalOptions{Deterministic: true}).Marshal(config)
	if err != nil {
		return nil, invalidGenerationConfig("encode payload", err)
	}
	if len(body) == 0 || len(body) > maxGenerationConfigBodySize {
		return nil, invalidGenerationConfig(fmt.Sprintf("encoded payload length %d is out of range", len(body)), nil)
	}
	return body, nil
}

func canonicalizeMessage(message protoreflect.Message, path string, depth int) error {
	if depth > maxTypedMessageDepth {
		return invalidGenerationConfig(path+" exceeds the nesting limit", nil)
	}
	if len(message.GetUnknown()) != 0 {
		return invalidGenerationConfig(path+" contains unknown protobuf fields", nil)
	}
	if typed, ok := message.Interface().(*serial.TypedMessage); ok {
		return canonicalizeTypedMessage(typed, path, depth)
	}

	var result error
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
			return true
		}
		fieldPath := path + "." + string(field.Name())
		switch {
		case field.IsList():
			list := value.List()
			for index := 0; index < list.Len(); index++ {
				if err := canonicalizeMessage(list.Get(index).Message(), fmt.Sprintf("%s[%d]", fieldPath, index), depth+1); err != nil {
					result = err
					return false
				}
			}
		case field.IsMap():
			if field.MapValue().Kind() == protoreflect.MessageKind || field.MapValue().Kind() == protoreflect.GroupKind {
				value.Map().Range(func(key protoreflect.MapKey, entry protoreflect.Value) bool {
					if err := canonicalizeMessage(entry.Message(), fmt.Sprintf("%s[%v]", fieldPath, key.Interface()), depth+1); err != nil {
						result = err
						return false
					}
					return true
				})
			}
		default:
			result = canonicalizeMessage(value.Message(), fieldPath, depth+1)
		}
		return result == nil
	})
	return result
}

func canonicalizeTypedMessage(typed *serial.TypedMessage, path string, depth int) error {
	if typed.Type == "" {
		return invalidGenerationConfig(path+" has an empty message type", nil)
	}
	messageType, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(typed.Type))
	if err != nil {
		return invalidGenerationConfig(path+" has an unregistered message type", err)
	}
	message := messageType.New().Interface()
	if err := (proto.UnmarshalOptions{RecursionLimit: maxTypedMessageDepth}).Unmarshal(typed.Value, message); err != nil {
		return invalidGenerationConfig(path+" has an invalid message value", err)
	}
	if err := canonicalizeMessage(message.ProtoReflect(), path+".value", depth+1); err != nil {
		return err
	}
	canonical, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		return invalidGenerationConfig(path+" value cannot be encoded", err)
	}
	typed.Value = canonical
	return nil
}

func typedMessageInstance(typed *serial.TypedMessage, path string) (proto.Message, error) {
	if typed == nil {
		return nil, invalidGenerationConfig(path+" is nil", nil)
	}
	messageType, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(typed.Type))
	if err != nil {
		return nil, invalidGenerationConfig(path+" has an unregistered message type", err)
	}
	message := messageType.New().Interface()
	if err := (proto.UnmarshalOptions{RecursionLimit: maxTypedMessageDepth}).Unmarshal(typed.Value, message); err != nil {
		return nil, invalidGenerationConfig(path+" has an invalid message value", err)
	}
	if err := canonicalizeMessage(message.ProtoReflect(), path+".value", 0); err != nil {
		return nil, err
	}
	return message, nil
}

func canonicalTypedMessage(message proto.Message) (*serial.TypedMessage, error) {
	if message == nil {
		return nil, invalidGenerationConfig("nil typed message", nil)
	}
	if err := canonicalizeMessage(message.ProtoReflect(), string(message.ProtoReflect().Descriptor().FullName()), 0); err != nil {
		return nil, err
	}
	value, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		return nil, invalidGenerationConfig("encode typed message", err)
	}
	return &serial.TypedMessage{Type: string(message.ProtoReflect().Descriptor().FullName()), Value: value}, nil
}

func validateGenerationReference(sourceTag, field, targetTag string, tags map[string]struct{}) error {
	if targetTag == "" {
		return nil
	}
	if _, ok := tags[targetTag]; !ok {
		return invalidGenerationConfig(fmt.Sprintf("outbound %q %s references non-generation tag %q", sourceTag, field, targetTag), nil)
	}
	return nil
}

func validateGenerationTag(tag string) error {
	if len(tag) == 0 || len(tag) > maxGenerationTagLength {
		return fmt.Errorf("length %d is out of range", len(tag))
	}
	for index := 0; index < len(tag); index++ {
		character := tag[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
			continue
		}
		if index > 0 && (character == '-' || character == '_' || character == '.' || character == ':' || character == '@') {
			continue
		}
		return fmt.Errorf("contains unsupported character %q at byte %d", character, index)
	}
	return nil
}

func validateGenerationOutboundTag(tag string) error {
	if err := validateGenerationTag(tag); err != nil {
		return fmt.Errorf("xrayrt: invalid outbound tag %q: %w", tag, err)
	}
	return nil
}

func cloneOutboundConfigs(outbounds []*core.OutboundHandlerConfig) []*core.OutboundHandlerConfig {
	cloned := make([]*core.OutboundHandlerConfig, len(outbounds))
	for index, outbound := range outbounds {
		if outbound != nil {
			cloned[index] = proto.Clone(outbound).(*core.OutboundHandlerConfig)
		}
	}
	return cloned
}

func invalidGenerationConfig(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrInvalidGenerationConfig, message)
	}
	return fmt.Errorf("%w: %s: %w", ErrInvalidGenerationConfig, message, cause)
}
