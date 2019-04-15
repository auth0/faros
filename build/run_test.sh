#!/bin/bash


set -o errexit
set -o nounset
set -o pipefail

go get -u github.com/onsi/ginkgo/ginkgo
go get -u github.com/onsi/gomega/...

# Prepare environment.
make prepare-env-1.11
export SKIP_DRY_RUN_TESTS=true
export KUBEBUILDER_CONTROLPLANE_START_TIMEOUT=300s

make test
