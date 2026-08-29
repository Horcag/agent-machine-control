#!/usr/bin/env sh
set -eu

. ./quality/tool-versions.env

require_literal() {
    file=$1
    literal=$2
    if ! grep -F -- "$literal" "$file" >/dev/null; then
        echo "tool version drift: $file does not contain $literal" >&2
        exit 1
    fi
}

require_literal .github/workflows/ci.yml "version: $GOLANGCI_LINT_VERSION"
require_literal .github/workflows/security.yml "version: $ZIZMOR_VERSION"
require_literal .github/workflows/release.yml "version: \"$GORELEASER_VERSION\""
require_literal .github/workflows/docs.yml "lycheeVersion: $LYCHEE_VERSION"
require_literal .mcp.json "code-review-graph==$CODE_REVIEW_GRAPH_VERSION"

echo "tool versions: consistent"
