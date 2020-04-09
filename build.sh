#!/bin/bash

set -e

CGO_ENABLED=0 go build -o tmp/videoproc-testing ./cmd/videoproc/
CGO_ENABLED=0 go build -o tmp/seeker ./cmd/seeker