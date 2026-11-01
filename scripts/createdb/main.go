// Command createdb creates a PostgreSQL database if it does not exist. It
// lets CI create databases the same way on Linux, macOS and Windows without
// depending on psql being on the PATH.
//
//	go run ./scripts/createdb "postgres://user@host:5432/postgres" name
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: createdb <admin-url> <name>")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer conn.Close(ctx)
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", os.Args[2]).Scan(&exists); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !exists {
		if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{os.Args[2]}.Sanitize()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	fmt.Println("database", os.Args[2], "ready")
}
