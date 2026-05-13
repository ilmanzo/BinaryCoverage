#!/bin/bash
set -e

echo "Running Go unit tests..."
go test -v ./...

echo "All tests passed."
