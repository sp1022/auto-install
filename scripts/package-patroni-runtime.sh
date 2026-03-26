#!/bin/bash

set -euo pipefail

usage() {
    cat <<'EOF'
Usage:
  package-patroni-runtime.sh <python_root_or_python_bin_or_tarball_or_url> <site_packages_dir> <output_tar_gz>

Examples:
  package-patroni-runtime.sh /opt/python3.11 /opt/python3.11/lib/python3.11/site-packages soft/patroni-runtime-linux-amd64.tar.gz
  package-patroni-runtime.sh /opt/python3.11/bin/python3 /opt/patroni-venv/lib/python3.11/site-packages soft/patroni-runtime-linux-amd64.tar.gz
  package-patroni-runtime.sh https://example.com/python-runtime.tar.gz /opt/patroni-venv/lib/python3.11/site-packages soft/patroni-runtime-linux-amd64.tar.gz
EOF
    exit 1
}

if [ "$#" -ne 3 ]; then
    usage
fi

PYTHON_SOURCE="$1"
SITE_PACKAGES_DIR="$2"
OUTPUT_FILE="$3"

WORK_DIR="$(mktemp -d)"
SOURCE_DIR="$(mktemp -d)"
DOWNLOAD_FILE=""
trap 'rm -rf "$WORK_DIR" "$SOURCE_DIR"; if [ -n "$DOWNLOAD_FILE" ]; then rm -f "$DOWNLOAD_FILE"; fi' EXIT

download_file() {
    local source="$1"
    local dest
    dest="$(mktemp /tmp/patroni-runtime-download.XXXXXX)"

    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$source" -o "$dest"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO "$dest" "$source"
    else
        echo "curl or wget is required to download $source" >&2
        exit 1
    fi

    DOWNLOAD_FILE="$dest"
    printf '%s\n' "$dest"
}

extract_tarball() {
    local archive="$1"
    local dest="$2"
    case "$archive" in
        *.tar.gz|*.tgz) tar -xzf "$archive" -C "$dest" ;;
        *.tar.xz|*.txz) tar -xJf "$archive" -C "$dest" ;;
        *.tar) tar -xf "$archive" -C "$dest" ;;
        *)
            echo "unsupported archive format: $archive" >&2
            exit 1
            ;;
    esac
}

find_python_root() {
    local source="$1"

    if [ -d "$source" ] && [ -x "$source/bin/python3" ]; then
        printf '%s\n' "$source"
        return
    fi

    if [ -f "$source" ] && [ -x "$source" ]; then
        printf '%s\n' "$(CDPATH= cd -- "$(dirname "$source")/.." && pwd)"
        return
    fi

    if [ -f "$source" ]; then
        extract_tarball "$source" "$SOURCE_DIR"
        local found
        found="$(find "$SOURCE_DIR" -path '*/bin/python3' | head -n 1 || true)"
        if [ -z "$found" ]; then
            echo "python3 not found in archive: $source" >&2
            exit 1
        fi
        printf '%s\n' "$(CDPATH= cd -- "$(dirname "$found")/.." && pwd)"
        return
    fi

    echo "python runtime source not found: $source" >&2
    exit 1
}

copy_python_tree() {
    local python_root="$1"
    for rel in bin lib include share; do
        if [ -e "$python_root/$rel" ]; then
            cp -R "$python_root/$rel" "$WORK_DIR/"
        fi
    done
}

merge_site_packages() {
    local source_site_packages="$1"
    local target="$WORK_DIR/lib/site-packages"
    local versioned_site

    if [ ! -d "$source_site_packages" ]; then
        echo "site-packages dir not found: $source_site_packages" >&2
        exit 1
    fi

    versioned_site="$(find "$WORK_DIR/lib" -maxdepth 2 -type d -path '*/python*/site-packages' | head -n 1 || true)"
    if [ -n "$versioned_site" ]; then
        target="$versioned_site"
    fi

    mkdir -p "$target"
    cp -R "$source_site_packages"/. "$target"/
}

patch_patronictl_compat() {
    local ctl_py
    ctl_py="$(find "$WORK_DIR/lib" -path '*/site-packages/patroni/ctl.py' | head -n 1 || true)"
    if [ -z "$ctl_py" ] || [ ! -f "$ctl_py" ]; then
        return
    fi

    python3 - "$ctl_py" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
content = path.read_text()
old = "from prettytable import ALL, FRAME, PrettyTable"
new = """try:
    from prettytable import HRuleStyle, PrettyTable
    ALL = HRuleStyle.ALL
    FRAME = HRuleStyle.FRAME
except ImportError:
    from prettytable import ALL, FRAME, PrettyTable"""

if old in content and new not in content:
    path.write_text(content.replace(old, new, 1))
PY
}

write_wrapper() {
    local module_name="$1"
    local output_path="$2"
    local exec_mode="module"

    if [ "$module_name" = "patroni.ctl" ]; then
        exec_mode="click"
    fi

    cat >"$output_path" <<EOF
#!/bin/sh
set -e
SELF="\$(readlink -f "\$0" 2>/dev/null || printf '%s\n' "\$0")"
DIR="\$(CDPATH= cd -- "\$(dirname "\$SELF")" && pwd)"
RUNTIME_ROOT="\$DIR/.."
PY_SITE="\$RUNTIME_ROOT/lib/site-packages"
PY_VERSIONED_SITE="\$(find "\$RUNTIME_ROOT/lib" -maxdepth 2 -type d -path '*/python*/site-packages' | head -n 1 || true)"
export PYTHONHOME="\$RUNTIME_ROOT"
if [ -n "\$PY_VERSIONED_SITE" ]; then
    export PYTHONPATH="\$PY_VERSIONED_SITE"
fi
if [ -d "\$PY_SITE" ]; then
    export PYTHONPATH="\${PYTHONPATH:+\$PYTHONPATH:}\$PY_SITE"
fi
export PYTHONWARNINGS="ignore::DeprecationWarning"
if [ "$exec_mode" = "click" ]; then
exec "\$DIR/python3" -c 'from patroni.ctl import ctl; ctl()' "\$@"
fi
exec "\$DIR/python3" -m $module_name "\$@"
EOF

    chmod +x "$output_path"
}

if [[ "$PYTHON_SOURCE" =~ ^https?:// ]]; then
    PYTHON_SOURCE="$(download_file "$PYTHON_SOURCE")"
fi

PYTHON_ROOT="$(find_python_root "$PYTHON_SOURCE")"

copy_python_tree "$PYTHON_ROOT"
merge_site_packages "$SITE_PACKAGES_DIR"
patch_patronictl_compat

if [ ! -x "$WORK_DIR/bin/python3" ] && [ -x "$WORK_DIR/bin/python" ]; then
    ln -sfn python "$WORK_DIR/bin/python3"
fi

if [ ! -x "$WORK_DIR/bin/python3" ]; then
    echo "python3 not found in runtime tree: $PYTHON_ROOT" >&2
    exit 1
fi

write_wrapper "patroni" "$WORK_DIR/bin/patroni"
write_wrapper "patroni.ctl" "$WORK_DIR/bin/patronictl"

chmod +x "$WORK_DIR/bin/python3"

mkdir -p "$(dirname "$OUTPUT_FILE")"
tar -czf "$OUTPUT_FILE" -C "$WORK_DIR" .
echo "Created $OUTPUT_FILE"
