#!/usr/bin/env sh
set -eu

. ./quality/coverage.env

profile=${1:-coverage.out}

if [ ! -f "$profile" ]; then
    echo "coverage error: profile not found: $profile" >&2
    exit 1
fi

awk \
    -v file_min="$COVERAGE_FILE_MIN" \
    -v package_min="$COVERAGE_PACKAGE_MIN" \
    -v total_min="$COVERAGE_TOTAL_MIN" '
    NR == 1 { next }
    {
        split($1, location, ":")
        file = location[1]
        if (file ~ /_generated\.go$/) {
            next
        }

        statements = $2
        count = $3
        file_total[file] += statements
        total += statements
        if (count > 0) {
            file_covered[file] += statements
            covered += statements
        }

        package = file
        sub(/\/[^/]+$/, "", package)
        package_total[package] += statements
        if (count > 0) {
            package_covered[package] += statements
        }
    }
    END {
        failed = 0
        if (total == 0) {
            print "coverage error: profile contains no statements" > "/dev/stderr"
            exit 1
        }

        for (file in file_total) {
            percent = 100 * file_covered[file] / file_total[file]
            if (percent + 0.0001 < file_min) {
                printf "coverage error: file %s is %.1f%%; minimum is %.1f%%\n", file, percent, file_min > "/dev/stderr"
                failed = 1
            }
        }

        for (package in package_total) {
            percent = 100 * package_covered[package] / package_total[package]
            if (percent + 0.0001 < package_min) {
                printf "coverage error: package %s is %.1f%%; minimum is %.1f%%\n", package, percent, package_min > "/dev/stderr"
                failed = 1
            }
        }

        percent = 100 * covered / total
        if (percent + 0.0001 < total_min) {
            printf "coverage error: total is %.1f%%; minimum is %.1f%%\n", percent, total_min > "/dev/stderr"
            failed = 1
        }

        if (failed) {
            exit 1
        }
        printf "coverage policy: passed (total %.1f%%, minimum %.1f%%)\n", percent, total_min
    }
' "$profile"
