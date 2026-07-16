package guppy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/fil-forge/smelt/pkg/stack"
)

// LoginViaEmail completes a forge email login for the agent running in
// service's container: it execs loginCmd (a blocking CLI login, e.g.
// "guppy login <email>" or "ingot login <email>") and races it against the
// SMTP4Dev email clicker, which fetches the access-request email from the
// stack's smtp4dev and POSTs the validation link from inside the same
// container — the link uses in-network DNS names that don't resolve on the
// host.
//
//	err := guppy.LoginViaEmail(ctx, s, "ingot", email,
//	    "ingot", "--config", "/etc/ingot/config.yaml", "login", email)
func LoginViaEmail(ctx context.Context, s *stack.Stack, service, email string, loginCmd ...string) error {
	if len(loginCmd) == 0 {
		return fmt.Errorf("login via email: empty login command")
	}

	validator, err := NewSMTP4DevLoginValidator(
		s.EmailEndpoint(),
		WithSMTP4DevLoginValidatorClicker(&ExecDoer{Stack: s, Service: service}),
	)
	if err != nil {
		return fmt.Errorf("email validator: %w", err)
	}

	lctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	g, gctx := errgroup.WithContext(lctx)
	g.Go(func() error {
		if _, stderr, err := s.Exec(gctx, service, loginCmd...); err != nil {
			return fmt.Errorf("%s: %w (stderr=%s)", strings.Join(loginCmd, " "), err, stderr)
		}
		return nil
	})
	g.Go(func() error {
		return validator.ValidateEmailLogin(gctx, email)
	})
	return g.Wait()
}
