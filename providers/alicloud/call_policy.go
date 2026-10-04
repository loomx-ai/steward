package alicloud

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/alibabacloud-go/tea/dara"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	cloudProductQueryTimeout    = 5 * time.Second
	cloudProductQueryRetryCount = 3
	cloudProductQueryRetryDelay = 250 * time.Millisecond
	// Tea pools one transport per endpoint and runtime option set, so an idle
	// timeout longer than the gap between pages keeps connections alive across
	// calls instead of dialing (and TLS handshaking) for nearly every request.
	cloudProductIdleTimeout   = 30 * time.Second
	cloudProductMaxIdleConns  = 16
	resourceCenterCallTimeout = 5 * time.Second
	resourceCenterRetryCount  = 3

	destructivePreconnectRetryCount = 2

	// Throttling outlasts the short transient retries above: a throttled
	// query backs off exponentially from one second, capped per wait, over
	// more attempts.
	throttledQueryRetryCount   = 5
	throttledQueryRetryDelay   = time.Second
	throttledQueryRetryCeiling = 20 * time.Second

	// enrichmentConcurrency bounds the per-item reads one inventory batch runs
	// at a time. Each read keeps its own retry and throttling backoff.
	enrichmentConcurrency = 4
)

// forEachConcurrently calls read for indexes 0..count-1, at most
// enrichmentConcurrency at a time. After a failure no further reads start, and
// the lowest failing index's error is returned, as a serial loop would.
func forEachConcurrently(count int, read func(int) error) error {
	errs := make([]error, count)
	var failed atomic.Bool
	var wg sync.WaitGroup
	slots := make(chan struct{}, enrichmentConcurrency)
	for index := range count {
		slots <- struct{}{}
		if failed.Load() {
			<-slots
			break
		}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			if errs[index] = read(index); errs[index] != nil {
				failed.Store(true)
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

type queryRetryObserver func(failedAttempt, nextAttempt int, err error)

func callCloudProductQuery[T any](
	ctx context.Context,
	call func(context.Context, *dara.RuntimeOptions) (T, error),
	observers ...queryRetryObserver,
) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	var lastErr error
	for attempt := 0; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, cloudProductQueryTimeout)
		result, err := call(attemptCtx, runtimeOptions(cloudProductQueryTimeout))
		cancel()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if attempt >= queryRetryLimit(cloudProductQueryRetryCount, err) || !isRetryableQueryError(err) {
			break
		}
		for _, observer := range observers {
			if observer != nil {
				observer(attempt+1, attempt+2, err)
			}
		}
		if err := waitForQueryRetry(ctx, queryRetryDelay(attempt, err)); err != nil {
			return zero, err
		}
	}
	return zero, lastErr
}

func callResourceCenterQuery[T any](
	ctx context.Context,
	call func(context.Context, *dara.RuntimeOptions) (T, error),
	observers ...queryRetryObserver,
) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	var lastErr error
	for attempt := 0; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, resourceCenterCallTimeout)
		result, err := call(attemptCtx, runtimeOptions(resourceCenterCallTimeout))
		cancel()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if attempt >= queryRetryLimit(resourceCenterRetryCount, err) || !isRetryableQueryError(err) {
			break
		}
		for _, observer := range observers {
			if observer != nil {
				observer(attempt+1, attempt+2, err)
			}
		}
		if err := waitForQueryRetry(ctx, queryRetryDelay(attempt, err)); err != nil {
			return zero, err
		}
	}
	return zero, lastErr
}

func callDestructiveProductAPI[T any](
	ctx context.Context,
	call func(context.Context, *dara.RuntimeOptions) (T, error),
	observers ...queryRetryObserver,
) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	var lastErr error
	for attempt := 0; attempt <= destructivePreconnectRetryCount; attempt++ {
		result, err := call(ctx, &dara.RuntimeOptions{})
		if err == nil {
			return result, nil
		}
		lastErr = err
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		// Retry only a failure from dial/connect, before an HTTP request can
		// be written. Read/write resets are outcome-ambiguous for deletes and
		// must be reconciled through provider readback instead.
		if attempt == destructivePreconnectRetryCount || !isPreconnectBadFileDescriptor(err) {
			break
		}
		for _, observer := range observers {
			if observer != nil {
				observer(attempt+1, attempt+2, err)
			}
		}
		if err := waitForQueryRetry(ctx, cloudProductQueryRetryDelay<<attempt); err != nil {
			return zero, err
		}
	}
	return zero, lastErr
}

func isPreconnectBadFileDescriptor(err error) bool {
	if err == nil {
		return false
	}
	var networkError *net.OpError
	if errors.As(err, &networkError) &&
		strings.EqualFold(strings.TrimSpace(networkError.Op), "dial") &&
		errors.Is(err, syscall.EBADF) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "dial tcp") &&
		strings.Contains(message, "connect: bad file descriptor")
}

func isRetryableQueryError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// A DNS NXDOMAIN response can be transient when the local resolver or its
	// upstream is briefly unavailable. Retry it before deciding whether the
	// product endpoint is unsupported in the requested region.
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return true
	}
	normalized := NormalizeError(err)
	var providerCall *contracts.ProviderCallError
	if !errors.As(normalized, &providerCall) {
		return true
	}
	switch providerCall.Provider.Category {
	case execution.ErrorRetryable, execution.ErrorThrottled, execution.ErrorProviderFailure:
		return true
	default:
		return false
	}
}

func queryThrottled(err error) bool {
	var providerCall *contracts.ProviderCallError
	return errors.As(NormalizeError(err), &providerCall) && providerCall.Provider.Category == execution.ErrorThrottled
}

func queryRetryLimit(retries int, err error) int {
	if queryThrottled(err) {
		return max(retries, throttledQueryRetryCount)
	}
	return retries
}

func queryRetryDelay(attempt int, err error) time.Duration {
	delay := cloudProductQueryRetryDelay << attempt
	if queryThrottled(err) {
		delay = min(throttledQueryRetryDelay<<attempt, throttledQueryRetryCeiling)
	}
	var providerCall *contracts.ProviderCallError
	if errors.As(NormalizeError(err), &providerCall) && providerCall.RetryAfter > delay {
		delay = providerCall.RetryAfter
	}
	// Independent jitter prevents workers scanning the same product across
	// regions from retrying in lockstep after provider throttling.
	return time.Duration(float64(delay) * (0.75 + rand.Float64()*0.5))
}

func waitForQueryRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func runtimeOptions(timeout time.Duration) *dara.RuntimeOptions {
	return &dara.RuntimeOptions{
		ReadTimeout:  dara.Int(int(timeout.Milliseconds())),
		IdleTimeout:  dara.Int(int(cloudProductIdleTimeout.Milliseconds())),
		MaxIdleConns: dara.Int(cloudProductMaxIdleConns),
	}
}
