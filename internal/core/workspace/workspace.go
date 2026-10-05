// Package workspace carries the tenant a piece of work belongs to. Every
// tenant row is stored under one workspace; a self-hosted server only ever
// uses Default.
package workspace

import (
	"context"
	"regexp"
)

type ID string

// Default is the only workspace of a self-hosted server.
const Default ID = "default"

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (id ID) Valid() bool { return validID.MatchString(string(id)) }

type contextKey struct{}

type scope struct {
	id  ID
	all bool
}

// With binds ctx to one workspace.
func With(ctx context.Context, id ID) context.Context {
	return context.WithValue(ctx, contextKey{}, scope{id: id})
}

// AcrossAll marks ctx for the few background reads that look for work in
// every workspace (job claims, due schedules). Whatever they return must be
// handled under With(ctx, its workspace).
func AcrossAll(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey{}, scope{all: true})
}

// From returns the workspace bound to ctx.
func From(ctx context.Context) (ID, bool) {
	value, ok := ctx.Value(contextKey{}).(scope)
	if !ok || value.all {
		return "", false
	}
	return value.id, true
}

// IsAcrossAll reports whether ctx was marked by AcrossAll.
func IsAcrossAll(ctx context.Context) bool {
	value, ok := ctx.Value(contextKey{}).(scope)
	return ok && value.all
}
