#!/usr/bin/env sh
set -eu

. ./quality/file-size.env

exceptions=./quality/file-size-exceptions.txt

exception_limit() {
    target=$1
    awk -F= -v target="$target" '
        $0 !~ /^#/ && NF == 2 && $1 == target { print $2; found = 1 }
        END { if (!found) print "" }
    ' "$exceptions"
}

default_limit() {
    if is_test_file "$1"; then
        echo "$TEST_MAX_LINES"
    else
        echo "$SOURCE_MAX_LINES"
    fi
}

warn_limit() {
    if is_test_file "$1"; then
        echo "$TEST_WARN_LINES"
    else
        echo "$SOURCE_WARN_LINES"
    fi
}

is_test_file() {
    case "$1" in
        *_test.go|*.test.ts|*.test.tsx|*.test.js|*.test.jsx|*.spec.ts|*.spec.tsx|*.spec.js|*.spec.jsx)
            return 0
            ;;
        *)
            return 1
            ;;
    esac
}

check_file() {
    file=$1
    lines=$(awk 'END { print NR }' "$file")
    maximum=$(default_limit "$file")
    warning=$(warn_limit "$file")
    exception=$(exception_limit "$file")

    if [ -n "$exception" ]; then
        maximum=$exception
    fi

    if [ "$lines" -gt "$maximum" ]; then
        echo "file-size error: $file has $lines lines; maximum is $maximum" >&2
        return 1
    fi
    if [ "$lines" -gt "$warning" ]; then
        echo "file-size warning: $file has $lines lines; warning starts at $warning" >&2
    fi
}

git ls-files --cached --others --exclude-standard \
    '*.go' '*.sh' '*.ps1' '*.ts' '*.tsx' '*.js' '*.jsx' '*.css' |
while IFS= read -r file; do
    case "$file" in
        cmd/*|internal/*|pkg/*|tools/*|scripts/*|web/src/*)
            check_file "$file"
            ;;
    esac
done

while IFS='=' read -r file maximum; do
    case "$file" in
        ''|'#'*) continue ;;
    esac

    if [ ! -f "$file" ]; then
        echo "file-size error: stale exception for missing file $file" >&2
        exit 1
    fi

    lines=$(awk 'END { print NR }' "$file")
    default=$(default_limit "$file")
    if [ "$lines" -le "$default" ]; then
        echo "file-size error: stale exception for $file; $lines lines is within default $default" >&2
        exit 1
    fi
    if [ "$maximum" -le "$default" ]; then
        echo "file-size error: exception for $file must exceed default $default" >&2
        exit 1
    fi
done < "$exceptions"

echo "file-size policy: passed"
