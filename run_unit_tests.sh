#!/bin/bash
set -e

echo "Running Go unit tests..."
go test -v -coverprofile=coverage.out ./...

echo "All tests passed."
echo ""
go tool cover -func=coverage.out | tail -1
