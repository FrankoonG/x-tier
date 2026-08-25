package xrayrt

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/core"
	featureoutbound "github.com/xtls/xray-core/features/outbound"

	_ "github.com/xtls/xray-core/app/proxyman/outbound"
)

type outboundHandlerFactory func(*core.Instance, *core.OutboundHandlerConfig) (featureoutbound.Handler, error)

// InstanceBackend dynamically owns generation-qualified outbound handlers in
// one caller-owned, long-lived Xray instance. It never starts or closes the
// instance itself.
type InstanceBackend struct {
	instance *core.Instance
	outbound featureoutbound.Manager
	create   outboundHandlerFactory

	buildMu     sync.Mutex
	lastAttempt uint64
}

func NewInstanceBackend(instance *core.Instance) (*InstanceBackend, error) {
	if instance == nil {
		return nil, errors.New("xrayrt: nil Xray instance")
	}
	if !instance.IsRunning() {
		return nil, errors.New("xrayrt: Xray instance must already be running")
	}
	feature := instance.GetFeature(featureoutbound.ManagerType())
	outboundManager, ok := feature.(featureoutbound.Manager)
	if !ok || outboundManager == nil {
		return nil, errors.New("xrayrt: Xray outbound manager is not registered")
	}
	return &InstanceBackend{
		instance: instance,
		outbound: outboundManager,
		create:   createOutboundHandler,
	}, nil
}

// NewInstanceManager binds generation management and forced-tag stream dialing
// to the same long-lived Xray instance. Closing the returned Manager drains its
// handlers but leaves the caller-owned instance running.
func NewInstanceManager(instance *core.Instance) (*Manager, error) {
	backend, err := NewInstanceBackend(instance)
	if err != nil {
		return nil, err
	}
	return NewManager(backend, NewXrayStreamDialer(instance)), nil
}

func (b *InstanceBackend) Build(ctx context.Context, generation uint64, encoded GenerationConfig) (Generation, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if b == nil || b.instance == nil || b.outbound == nil || b.create == nil {
		return nil, errors.New("xrayrt: incomplete instance backend")
	}
	if !b.instance.IsRunning() {
		return nil, errors.New("xrayrt: Xray instance is not running")
	}
	if generation == 0 {
		return nil, errors.New("xrayrt: generation must be greater than zero")
	}

	b.buildMu.Lock()
	defer b.buildMu.Unlock()
	if generation <= b.lastAttempt {
		return nil, fmt.Errorf("xrayrt: generation %d is not newer than %d", generation, b.lastAttempt)
	}
	b.lastAttempt = generation

	config, err := encoded.decode()
	if err != nil {
		return nil, err
	}
	qualified, aliases, err := qualifyOutboundConfigs(generation, config.Outbound)
	if err != nil {
		return nil, err
	}

	handle := &instanceGeneration{
		backend: b,
		id:      generation,
		aliases: aliases,
	}
	for index, handlerConfig := range qualified {
		if err := ctx.Err(); err != nil {
			return b.failedBuildGeneration(handle, err)
		}
		handler, createErr := b.create(b.instance, handlerConfig)
		if createErr != nil {
			return b.failedBuildGeneration(handle,
				fmt.Errorf("xrayrt: create generation %d outbound %q: %w", generation, handlerConfig.Tag, createErr))
		}
		if handler == nil {
			return b.failedBuildGeneration(handle,
				fmt.Errorf("xrayrt: create generation %d outbound %q returned nil", generation, handlerConfig.Tag))
		}
		handle.handlers = append(handle.handlers, installedHandler{handler: handler, unregistered: true})
		if handler.Tag() != handlerConfig.Tag {
			return b.failedBuildGeneration(handle,
				fmt.Errorf("xrayrt: generation %d outbound %d created tag %q, want %q", generation, index, handler.Tag(), handlerConfig.Tag))
		}
	}

	for index := range handle.handlers {
		if err := ctx.Err(); err != nil {
			return b.failedBuildGeneration(handle, err)
		}
		entry := &handle.handlers[index]
		handler := entry.handler
		if err := b.outbound.AddHandler(ctx, handler); err != nil {
			if sameHandler(b.outbound.GetHandler(handler.Tag()), handler) {
				entry.unregistered = false
			}
			return b.failedBuildGeneration(handle,
				fmt.Errorf("xrayrt: install generation %d outbound %q: %w", generation, handler.Tag(), err))
		}
		entry.unregistered = false
	}

	if err := ctx.Err(); err != nil {
		return b.failedBuildGeneration(handle, err)
	}
	return handle, nil
}

func (b *InstanceBackend) failedBuildGeneration(handle *instanceGeneration, cause error) (Generation, error) {
	cleanupErr := b.Remove(handle)
	if cleanupErr == nil {
		return nil, cause
	}
	return handle, errors.Join(cause, cleanupErr)
}

// Remove unregisters all generation handlers before explicitly closing each
// handler. Successful steps are remembered and failed steps can be retried.
func (b *InstanceBackend) Remove(generation Generation) error {
	current, ok := generation.(*instanceGeneration)
	if !ok || current == nil || current.backend != b {
		return errors.New("xrayrt: generation does not belong to this backend")
	}
	current.removeMu.Lock()
	defer current.removeMu.Unlock()
	var result error
	for index := len(current.handlers) - 1; index >= 0; index-- {
		entry := &current.handlers[index]
		if !entry.unregistered {
			registered := b.outbound.GetHandler(entry.handler.Tag())
			switch {
			case registered == nil:
				entry.unregistered = true
			case !sameHandler(registered, entry.handler):
				result = errors.Join(result, fmt.Errorf("xrayrt: outbound %q was replaced before removal", entry.handler.Tag()))
			default:
				if err := b.outbound.RemoveHandler(context.Background(), entry.handler.Tag()); err != nil {
					result = errors.Join(result, fmt.Errorf("xrayrt: remove outbound %q: %w", entry.handler.Tag(), err))
					continue
				}
				entry.unregistered = true
			}
		}
		if !entry.closed {
			closeErr := entry.handler.Close()
			if closeErr != nil {
				result = errors.Join(result, fmt.Errorf("xrayrt: close outbound %q: %w", entry.handler.Tag(), closeErr))
			} else {
				entry.closed = true
			}
		}
	}
	return result
}

type instanceGeneration struct {
	backend  *InstanceBackend
	id       uint64
	aliases  map[string]string
	handlers []installedHandler

	removeMu sync.Mutex
}

type installedHandler struct {
	handler      featureoutbound.Handler
	unregistered bool
	closed       bool
}

func (g *instanceGeneration) OutboundTag(tag string) (string, error) {
	if g == nil {
		return "", errors.New("xrayrt: nil generation")
	}
	if err := validateGenerationOutboundTag(tag); err != nil {
		return "", err
	}
	qualified, ok := g.aliases[tag]
	if !ok {
		return "", fmt.Errorf("xrayrt: generation %d has no outbound %q", g.id, tag)
	}
	return qualified, nil
}

func sameHandler(left, right featureoutbound.Handler) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftValue := reflect.ValueOf(left)
	rightValue := reflect.ValueOf(right)
	if leftValue.Type() != rightValue.Type() || !leftValue.Type().Comparable() {
		return false
	}
	return leftValue.Interface() == rightValue.Interface()
}

func createOutboundHandler(instance *core.Instance, config *core.OutboundHandlerConfig) (featureoutbound.Handler, error) {
	object, err := core.CreateObject(instance, config)
	if err != nil {
		return nil, err
	}
	handler, ok := object.(featureoutbound.Handler)
	if !ok {
		return nil, errors.New("xrayrt: constructed object is not an outbound handler")
	}
	return handler, nil
}

func qualifyOutboundConfigs(generation uint64, outbounds []*core.OutboundHandlerConfig) ([]*core.OutboundHandlerConfig, map[string]string, error) {
	aliases := make(map[string]string, len(outbounds))
	for _, outbound := range outbounds {
		aliases[outbound.Tag] = qualifiedGenerationTag(generation, outbound.Tag)
	}

	qualified := cloneOutboundConfigs(outbounds)
	for _, outbound := range qualified {
		logicalTag := outbound.Tag
		outbound.Tag = aliases[logicalTag]
		if outbound.SenderSettings == nil {
			continue
		}
		message, err := typedMessageInstance(outbound.SenderSettings, "outbound "+logicalTag+" sender_settings")
		if err != nil {
			return nil, nil, err
		}
		sender, ok := message.(*proxyman.SenderConfig)
		if !ok {
			return nil, nil, fmt.Errorf("xrayrt: outbound %q sender settings are not proxyman.SenderConfig", logicalTag)
		}
		if sender.ProxySettings != nil && sender.ProxySettings.Tag != "" {
			sender.ProxySettings.Tag = aliases[sender.ProxySettings.Tag]
		}
		if sender.StreamSettings != nil && sender.StreamSettings.SocketSettings != nil && sender.StreamSettings.SocketSettings.DialerProxy != "" {
			sender.StreamSettings.SocketSettings.DialerProxy = aliases[sender.StreamSettings.SocketSettings.DialerProxy]
		}
		outbound.SenderSettings, err = canonicalTypedMessage(sender)
		if err != nil {
			return nil, nil, err
		}
	}
	return qualified, aliases, nil
}

func qualifiedGenerationTag(generation uint64, logicalTag string) string {
	return fmt.Sprintf("%s%020d/%s", generationTagPrefix, generation, logicalTag)
}
