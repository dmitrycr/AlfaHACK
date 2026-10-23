package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"

	"alfa.local/api-gateway/internal/repository/postgres"
	"alfa.local/shared/platform"
)

func runOperator(ctx context.Context, store postgres.Store, args []string) error {
	if len(args) == 0 {
		return errors.New("operator command required")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	email := flags.String("email", "", "mock recipient email")
	user := flags.String("user-id", "", "registration user ID")
	operator := flags.String("operator", "", "operator identifier for audit")
	if e := flags.Parse(args[1:]); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	switch args[0] {
	case "mock-email":
		if platform.Env("APP_ENV", "development") == "production" || platform.Env("EMAIL_DELIVERY_MODE", "mock") != "mock" {
			return errors.New("mock email is disabled")
		}
		if *email == "" {
			return errors.New("--email is required")
		}
		token, e := store.MockVerificationToken(ctx, *email)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"email": *email, "token": token, "delivery": "mock"})
	case "approve-registration", "revoke-access":
		if *user == "" || *operator == "" {
			return errors.New("--user-id and --operator are required")
		}
		if e := store.DecideMembership(ctx, *user, *operator, args[0] == "approve-registration"); e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"user_id": *user, "action": args[0], "status": "completed"})
	default:
		return errors.New("unknown operator command")
	}
}
