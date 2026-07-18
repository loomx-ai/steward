package alicloud

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"strings"
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
	cloudProductIdleTimeout     = time.Second
	resourceCenterCallTimeout   = 5 * time.Second
	resourceCenterRetryCount    = 3

	destructivePreconnectRetryCount = 2
)

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
	for attempt := 0; attempt <= cloudProductQueryRetryCount; attempt++ {
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
		if attempt == cloudProductQueryRetryCount || !isRetryableQueryError(err) {
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
	for attempt := 0; attempt <= resourceCenterRetryCount; attempt++ {
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
		if attempt == resourceCenterRetryCount || !isRetryableQueryError(err) {
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

func queryRetryDelay(attempt int, err error) time.Duration {
	delay := cloudProductQueryRetryDelay << attempt
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
		ReadTimeout: dara.Int(int(timeout.Milliseconds())),
		IdleTimeout: dara.Int(int(cloudProductIdleTimeout.Milliseconds())),
	}
}
