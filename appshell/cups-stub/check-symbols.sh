#!/bin/sh
# Compare the CUPS functions libcef.so needs with the ones the stub defines.
# Usage: check-symbols.sh path/to/libcef.so path/to/libcups.so.2
set -eu
need=$(nm -D --undefined-only "$1" | awk '{print $2}' | grep -E '^(cups|ipp|ppd|http)[A-Z]' | sort)
have=$(nm -D --defined-only "$2" | awk '{print $3}' | grep -E '^(cups|ipp|ppd|http)[A-Z]' | sort)
missing=$(printf '%s\n' "$need" | grep -vxF -f /dev/fd/3 3<<EOF || true
$have
EOF
)
if [ -n "$missing" ]; then
	echo "libcups stub is missing:" >&2
	printf '  %s\n' $missing >&2
	exit 1
fi
echo "libcups stub: all $(printf '%s\n' "$need" | wc -l) functions present"
