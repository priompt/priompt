package server

import (
	"context"
	"errors"
	"log"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	store "priomptdb"
)

// storeError translates a store failure into a canonical gRPC status.
//
// Two rules, and both matter. The code must be the one a client can act on:
// a moved branch is ABORTED (retry), an unreachable database is UNAVAILABLE
// (retry later), a missing row is NOT_FOUND (do not retry) — INTERNAL for all
// of them tells a caller nothing and makes correct retry behaviour impossible.
// And the driver's own message must not cross the wire: it carries table and
// column names, SQLite extended error codes, and — for a DSN failure — the
// database user, database name, host and resolver address. That is schema and
// infrastructure disclosure to any authenticated caller. The detail is logged
// server-side, where operators can see it, and the client gets a stable
// sentence.
func storeError(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "request deadline exceeded")
	case errors.Is(err, store.ErrNotFound):
		return status.Error(codes.NotFound, "not found")
	case errors.Is(err, store.ErrBranchNotFound):
		return status.Error(codes.NotFound, "branch not found")
	case errors.Is(err, store.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, "branch already exists")
	case errors.Is(err, store.ErrConflict):
		return status.Error(codes.Aborted,
			"the branch moved while this write was in flight; re-read and retry")
	case isUnavailable(err):
		log.Printf("%s: database unavailable: %v", op, err)
		return status.Error(codes.Unavailable, "storage backend unavailable")
	}
	log.Printf("%s: %v", op, err)
	return status.Errorf(codes.Internal, "%s failed", op)
}

// isUnavailable spots the transport-level failures worth marking retryable.
// Driver errors are not a typed hierarchy across backends, so this matches on
// text; a false negative only costs a less precise code, never correctness.
func isUnavailable(err error) bool {
	s := strings.ToLower(err.Error())
	for _, m := range []string{
		"connection refused", "no such host", "connection reset",
		"i/o timeout", "server closed the connection", "failed to connect",
		"database is locked", "too many connections", "driver: bad connection",
	} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}
