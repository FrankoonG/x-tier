package xrayrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	featureinbound "github.com/xtls/xray-core/features/inbound"
	featureoutbound "github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/proxy/blackhole"
	"google.golang.org/protobuf/proto"

	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	_ "github.com/xtls/xray-core/proxy/blackhole"
)

const (
	blockedDefaultTag      = "x-tier-blocked-default"
	inboundRecoveryTimeout = 5 * time.Second
	runtimeShutdownTimeout = 15 * time.Second
	forceShutdownTimeout   = 5 * time.Second
)

var ErrShutdownForced = errors.New("xrayrt: shutdown required force-closing the Xray core")

// Runtime owns X-Tier's single long-lived Xray instance. Generation changes
// add and drain handlers inside this instance; they never replace it.
type Runtime struct {
	instance *core.Instance
	manager  *Manager
	inbound  featureinbound.Manager

	inboundMu      sync.Mutex
	inboundConfigs []*core.InboundHandlerConfig
	inboundWire    []byte

	closeGate contextGate
	closeMu   sync.Mutex
	closeDone chan struct{}
	closing   bool
	closed    bool
	closeErr  error
}

type StartOptions struct {
	DefaultOutbound featureoutbound.Handler
	FixedOutbounds  []featureoutbound.Handler
	Inbounds        []*core.InboundHandlerConfig
}

func StartRuntime(ctx context.Context, options ...StartOptions) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("xrayrt: nil runtime context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(options) > 1 {
		return nil, errors.New("xrayrt: multiple start options")
	}
	var opts StartOptions
	if len(options) == 1 {
		opts = options[0]
	}
	wire, err := canonicalInboundWire(opts.Inbounds)
	if err != nil {
		return nil, err
	}
	instance, err := core.NewWithContext(ctx, baseRuntimeConfig(opts))
	if err != nil {
		return nil, fmt.Errorf("xrayrt: create Xray instance: %w", err)
	}
	if opts.DefaultOutbound != nil {
		feature := instance.GetFeature(featureoutbound.ManagerType())
		outboundManager, ok := feature.(featureoutbound.Manager)
		if !ok || outboundManager == nil {
			_ = instance.Close()
			return nil, errors.New("xrayrt: Xray outbound manager is not registered")
		}
		if err := outboundManager.AddHandler(ctx, opts.DefaultOutbound); err != nil {
			_ = instance.Close()
			return nil, fmt.Errorf("xrayrt: install default outbound: %w", err)
		}
		for index, handler := range opts.FixedOutbounds {
			if handler == nil {
				_ = instance.Close()
				return nil, fmt.Errorf("xrayrt: fixed outbound %d is nil", index)
			}
			if err := outboundManager.AddHandler(ctx, handler); err != nil {
				_ = instance.Close()
				return nil, fmt.Errorf("xrayrt: install fixed outbound %q: %w", handler.Tag(), err)
			}
		}
	} else if len(opts.FixedOutbounds) != 0 {
		_ = instance.Close()
		return nil, errors.New("xrayrt: fixed outbounds require a default outbound")
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		return nil, fmt.Errorf("xrayrt: start Xray instance: %w", err)
	}
	manager, err := NewInstanceManager(instance)
	if err != nil {
		_ = instance.Close()
		return nil, err
	}
	inboundFeature := instance.GetFeature(featureinbound.ManagerType())
	inboundManager, ok := inboundFeature.(featureinbound.Manager)
	if !ok || inboundManager == nil {
		_ = manager.CloseNow()
		_ = instance.Close()
		return nil, errors.New("xrayrt: Xray inbound manager is not registered")
	}
	return &Runtime{
		instance:       instance,
		manager:        manager,
		inbound:        inboundManager,
		inboundConfigs: cloneInboundConfigs(opts.Inbounds),
		inboundWire:    wire,
	}, nil
}

func (r *Runtime) Apply(ctx context.Context, config GenerationConfig) (uint64, error) {
	if r == nil || r.manager == nil {
		return 0, ErrClosed
	}
	return r.manager.Apply(ctx, config)
}

func (r *Runtime) RetryCleanup() error {
	if r == nil || r.manager == nil {
		return ErrClosed
	}
	return r.manager.RetryCleanup()
}

// EquivalentInbounds compares two complete desired inbound sets using the
// same canonical encoding that ReplaceInbounds uses for no-op detection.
func EquivalentInbounds(left, right []*core.InboundHandlerConfig) (bool, error) {
	leftWire, err := canonicalInboundWire(left)
	if err != nil {
		return false, err
	}
	rightWire, err := canonicalInboundWire(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftWire, rightWire), nil
}

func (r *Runtime) Dial(ctx context.Context, outboundTag, address string) (net.Conn, error) {
	if r == nil || r.manager == nil {
		return nil, ErrClosed
	}
	return r.manager.Dial(ctx, outboundTag, "tcp", address)
}

// DialFixed uses a process-lifetime handler that is not part of a reloadable
// generation. It is reserved for X-Tier-owned internal handlers.
func (r *Runtime) DialFixed(ctx context.Context, outboundTag, address string) (net.Conn, error) {
	if r == nil || r.instance == nil || !r.instance.IsRunning() {
		return nil, ErrClosed
	}
	return NewXrayStreamDialer(r.instance)(ctx, outboundTag, "tcp", address)
}

// ReplaceInbounds installs a complete desired inbound set in the same Xray
// instance. Listener replacement may be briefly disruptive when an address is
// reused; outbound generations and existing Rendr sessions remain untouched.
func (r *Runtime) ReplaceInbounds(ctx context.Context, configs []*core.InboundHandlerConfig) error {
	if r == nil || r.instance == nil || r.inbound == nil {
		return ErrClosed
	}
	if ctx == nil {
		return errors.New("xrayrt: nil inbound context")
	}
	wire, err := canonicalInboundWire(configs)
	if err != nil {
		return err
	}
	if err := r.closeGate.lock(ctx); err != nil {
		return err
	}
	defer r.closeGate.unlock()
	r.closeMu.Lock()
	closing := r.closing || r.closed
	r.closeMu.Unlock()
	if closing || !r.instance.IsRunning() {
		return ErrClosed
	}

	r.inboundMu.Lock()
	defer r.inboundMu.Unlock()
	actualTags := handlerTags(r.inbound.ListHandlers(ctx))
	if bytes.Equal(wire, r.inboundWire) && equalTags(actualTags, inboundTags(configs)) {
		return nil
	}
	candidates, err := r.createInboundHandlers(configs)
	if err != nil {
		return err
	}
	oldConfigs := cloneInboundConfigs(r.inboundConfigs)
	oldTags := actualTags
	allTags := appendUniqueTags(oldTags, inboundTags(configs)...)
	for _, tag := range oldTags {
		if err := r.inbound.RemoveHandler(ctx, tag); err != nil {
			cause := fmt.Errorf("xrayrt: remove inbound %q: %w", tag, err)
			disposeErr := closeInboundHandlers(candidates)
			return errors.Join(r.restoreOrFailStopped(oldConfigs, allTags, cause), disposeErr)
		}
	}
	installed := make(map[string]bool, len(candidates))
	for _, handler := range candidates {
		if err := r.inbound.AddHandler(ctx, handler); err != nil {
			if current, getErr := r.inbound.GetHandler(ctx, handler.Tag()); getErr == nil && current == handler {
				installed[handler.Tag()] = true
			}
			cause := fmt.Errorf("xrayrt: install inbound %q: %w", handler.Tag(), err)
			disposeErr := closeUninstalledInboundHandlers(candidates, installed)
			return errors.Join(r.restoreOrFailStopped(oldConfigs, allTags, cause), disposeErr)
		}
		installed[handler.Tag()] = true
	}
	r.inboundConfigs = cloneInboundConfigs(configs)
	r.inboundWire = bytes.Clone(wire)
	return nil
}

func (r *Runtime) Status() Status {
	if r == nil || r.manager == nil {
		return Status{Closed: true, StrictStreamOutbound: true, Draining: []GenerationStatus{}}
	}
	return r.manager.Status()
}

// ManagedInboundTags returns the inbound handlers represented by the last
// successful install or restore operation. A fail-stopped replacement clears
// this set, so callers never mistake desired listeners for active listeners.
func (r *Runtime) ManagedInboundTags() []string {
	if r == nil || r.instance == nil || !r.instance.IsRunning() {
		return []string{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r.inboundMu.Lock()
	tags := handlerTags(r.inbound.ListHandlers(ctx))
	r.inboundMu.Unlock()
	sort.Strings(tags)
	return tags
}

func handlerTags(handlers []featureinbound.Handler) []string {
	tags := make([]string, 0, len(handlers))
	for _, handler := range handlers {
		if handler != nil {
			tags = append(tags, handler.Tag())
		}
	}
	sort.Strings(tags)
	return tags
}

func equalTags(left, right []string) bool {
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	return slices.Equal(left, right)
}

// Close force-closes the process runtime after generation bookkeeping is
// closed. Hot reload drain happens before this boundary; daemon shutdown may
// terminate any remaining leased connections.
func (r *Runtime) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), runtimeShutdownTimeout)
	defer cancel()
	return r.CloseContext(ctx)
}

// CloseContext only closes the Xray core after generation work is quiescent.
// A deadline can bound an attempt, but an incomplete attempt leaves the core
// alive and may be retried with the same Runtime.
func (r *Runtime) CloseContext(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return errors.Join(ErrShutdownIncomplete, errors.New("xrayrt: nil runtime shutdown context"))
	}
	if err := r.closeGate.lock(ctx); err != nil {
		return errors.Join(ErrShutdownIncomplete, err)
	}

	r.closeMu.Lock()
	if r.closing {
		done := r.closeDone
		r.closeMu.Unlock()
		r.closeGate.unlock()
		return r.waitForClose(ctx, done)
	}
	r.closeMu.Unlock()

	var managerErr error
	if r.manager != nil {
		managerErr = r.manager.CloseNowContext(ctx)
		if errors.Is(managerErr, ErrShutdownIncomplete) {
			r.closeGate.unlock()
			return managerErr
		}
	}
	if err := ctx.Err(); err != nil {
		r.closeGate.unlock()
		return errors.Join(managerErr, ErrShutdownIncomplete, err)
	}

	r.closeMu.Lock()
	r.closing = true
	r.closeDone = make(chan struct{})
	done := r.closeDone
	r.closeMu.Unlock()
	go r.closeCore(managerErr, done)
	r.closeGate.unlock()
	return r.waitForClose(ctx, done)
}

// ForceClose closes the process-owned Xray core even when generation cleanup
// cannot reach quiescence. It is a final daemon-shutdown boundary, not a hot
// reload cleanup mechanism.
func (r *Runtime) ForceClose() error {
	ctx, cancel := context.WithTimeout(context.Background(), forceShutdownTimeout)
	defer cancel()
	return r.ForceCloseContext(ctx)
}

// ForceCloseContext first gives generation bookkeeping one final bounded
// cleanup attempt, then closes the core regardless of that result. The forced
// sentinel remains in the returned error so process supervisors can observe
// that shutdown was not graceful.
func (r *Runtime) ForceCloseContext(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return errors.Join(ErrShutdownForced, ErrShutdownIncomplete, errors.New("xrayrt: nil forced shutdown context"))
	}
	gateErr := r.closeGate.lock(ctx)
	if gateErr != nil {
		done := r.beginCoreClose(errors.Join(ErrShutdownForced, ErrShutdownIncomplete, gateErr))
		return errors.Join(ErrShutdownForced, gateErr, r.waitForClose(ctx, done))
	}

	r.closeMu.Lock()
	if r.closed {
		err := r.closeErr
		r.closeMu.Unlock()
		r.closeGate.unlock()
		return err
	}
	if r.closing {
		done := r.closeDone
		r.closeMu.Unlock()
		r.closeGate.unlock()
		return r.waitForClose(ctx, done)
	}
	r.closeMu.Unlock()

	var managerErr error
	if r.manager != nil {
		managerErr = r.manager.CloseNowContext(ctx)
	}
	done := r.beginCoreClose(errors.Join(ErrShutdownForced, managerErr))
	r.closeGate.unlock()
	return r.waitForClose(ctx, done)
}

func (r *Runtime) beginCoreClose(cause error) <-chan struct{} {
	r.closeMu.Lock()
	defer r.closeMu.Unlock()
	if r.closing {
		return r.closeDone
	}
	if r.closed {
		done := make(chan struct{})
		close(done)
		return done
	}
	r.closing = true
	r.closeDone = make(chan struct{})
	done := r.closeDone
	go r.closeCore(cause, done)
	return done
}

func (r *Runtime) closeCore(managerErr error, done chan struct{}) {
	closeErr := managerErr
	if r.instance != nil {
		closeErr = errors.Join(closeErr, r.instance.Close())
	}
	r.closeMu.Lock()
	r.closeErr = closeErr
	r.closed = true
	close(done)
	r.closeMu.Unlock()
}

func (r *Runtime) waitForClose(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		r.closeMu.Lock()
		defer r.closeMu.Unlock()
		return r.closeErr
	case <-ctx.Done():
		return errors.Join(ErrShutdownIncomplete, ctx.Err())
	}
}

func baseRuntimeConfig(opts StartOptions) *core.Config {
	config := &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
		},
		Inbound: cloneInboundConfigs(opts.Inbounds),
	}
	if opts.DefaultOutbound == nil {
		config.Outbound = []*core.OutboundHandlerConfig{{
			Tag:           blockedDefaultTag,
			ProxySettings: serial.ToTypedMessage(&blackhole.Config{}),
		}}
	}
	return config
}

func (r *Runtime) createInboundHandlers(configs []*core.InboundHandlerConfig) ([]featureinbound.Handler, error) {
	handlers := make([]featureinbound.Handler, 0, len(configs))
	for _, config := range configs {
		object, err := core.CreateObject(r.instance, proto.Clone(config).(*core.InboundHandlerConfig))
		if err != nil {
			return nil, errors.Join(fmt.Errorf("xrayrt: create inbound %q: %w", config.Tag, err), closeInboundHandlers(handlers))
		}
		handler, ok := object.(featureinbound.Handler)
		if !ok || handler == nil {
			return nil, errors.Join(fmt.Errorf("xrayrt: inbound %q did not create a handler", config.Tag), closeInboundHandlers(handlers))
		}
		handlers = append(handlers, handler)
	}
	return handlers, nil
}

func (r *Runtime) restoreOrFailStopped(configs []*core.InboundHandlerConfig, managedTags []string, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), inboundRecoveryTimeout)
	defer cancel()
	cleanupErr := r.stopInboundTags(ctx, managedTags)
	handlers, err := r.createInboundHandlers(configs)
	if err == nil {
		installed := make(map[string]bool, len(handlers))
		for _, handler := range handlers {
			addErr := r.inbound.AddHandler(ctx, handler)
			if addErr != nil {
				if current, getErr := r.inbound.GetHandler(ctx, handler.Tag()); getErr == nil && current == handler {
					installed[handler.Tag()] = true
				}
				err = fmt.Errorf("xrayrt: restore inbound %q: %w", handler.Tag(), addErr)
				break
			}
			installed[handler.Tag()] = true
		}
		if err == nil {
			r.inboundConfigs = cloneInboundConfigs(configs)
			r.inboundWire, _ = canonicalInboundWire(configs)
			return errors.Join(cause, cleanupErr)
		}
		cleanupErr = errors.Join(cleanupErr, r.stopInboundTags(ctx, managedTags), closeUninstalledInboundHandlers(handlers, installed))
	} else {
		err = fmt.Errorf("xrayrt: rebuild previous inbounds: %w", err)
	}
	r.inboundConfigs = nil
	r.inboundWire = nil
	cleanupErr = errors.Join(cleanupErr, r.stopInboundTags(ctx, managedTags))
	return errors.Join(cause, ErrInboundFailStopped, err, cleanupErr)
}

func (r *Runtime) stopInboundTags(ctx context.Context, tags []string) error {
	wanted := make(map[string]bool, len(tags))
	for _, tag := range tags {
		wanted[tag] = true
	}
	var result error
	for _, handler := range r.inbound.ListHandlers(ctx) {
		if handler == nil || !wanted[handler.Tag()] {
			continue
		}
		if err := r.inbound.RemoveHandler(ctx, handler.Tag()); err != nil {
			result = errors.Join(result, fmt.Errorf("xrayrt: stop inbound %q: %w", handler.Tag(), err), handler.Close())
		}
	}
	return result
}

func canonicalInboundWire(configs []*core.InboundHandlerConfig) ([]byte, error) {
	cloned := cloneInboundConfigs(configs)
	tags := make(map[string]struct{}, len(cloned))
	for index, config := range cloned {
		if config == nil {
			return nil, fmt.Errorf("xrayrt: inbound[%d] is nil", index)
		}
		if err := validateGenerationTag(config.Tag); err != nil {
			return nil, fmt.Errorf("xrayrt: invalid inbound tag %q: %w", config.Tag, err)
		}
		if _, exists := tags[config.Tag]; exists {
			return nil, fmt.Errorf("xrayrt: duplicate inbound tag %q", config.Tag)
		}
		tags[config.Tag] = struct{}{}
		if config.ReceiverSettings == nil || config.ProxySettings == nil {
			return nil, fmt.Errorf("xrayrt: inbound %q settings are incomplete", config.Tag)
		}
		if _, err := typedMessageInstance(config.ReceiverSettings, "inbound "+config.Tag+" receiver_settings"); err != nil {
			return nil, err
		}
		if _, err := typedMessageInstance(config.ProxySettings, "inbound "+config.Tag+" proxy_settings"); err != nil {
			return nil, err
		}
	}
	wire, err := (proto.MarshalOptions{Deterministic: true}).Marshal(&core.Config{Inbound: cloned})
	if err != nil {
		return nil, fmt.Errorf("xrayrt: encode inbounds: %w", err)
	}
	return wire, nil
}

func cloneInboundConfigs(configs []*core.InboundHandlerConfig) []*core.InboundHandlerConfig {
	cloned := make([]*core.InboundHandlerConfig, len(configs))
	for index, config := range configs {
		if config != nil {
			cloned[index] = proto.Clone(config).(*core.InboundHandlerConfig)
		}
	}
	return cloned
}

func inboundTags(configs []*core.InboundHandlerConfig) []string {
	tags := make([]string, 0, len(configs))
	for _, config := range configs {
		if config != nil && config.Tag != "" {
			tags = append(tags, config.Tag)
		}
	}
	return tags
}

func appendUniqueTags(tags []string, more ...string) []string {
	result := append([]string(nil), tags...)
	seen := make(map[string]bool, len(result)+len(more))
	for _, tag := range result {
		seen[tag] = true
	}
	for _, tag := range more {
		if !seen[tag] {
			result = append(result, tag)
			seen[tag] = true
		}
	}
	return result
}

func closeInboundHandlers(handlers []featureinbound.Handler) error {
	var result error
	for _, handler := range handlers {
		if handler != nil {
			result = errors.Join(result, handler.Close())
		}
	}
	return result
}

func closeUninstalledInboundHandlers(handlers []featureinbound.Handler, installed map[string]bool) error {
	var result error
	for _, handler := range handlers {
		if handler != nil && !installed[handler.Tag()] {
			result = errors.Join(result, handler.Close())
		}
	}
	return result
}
