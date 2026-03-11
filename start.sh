#!/bin/bash
set -e

echo "🤖 AutoTo 啟動中..."
echo ""

AUTOTO_PORT="${AUTOTO_PORT:-5678}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PYTHON_CMD=""
ORIGINAL_ARGS=("$@")
EFFECTIVE_PORT="$AUTOTO_PORT"

if command -v python3.11 >/dev/null 2>&1; then
    PYTHON_CMD="python3.11"
elif command -v python3 >/dev/null 2>&1 && python3 -c 'import sys; raise SystemExit(0 if sys.version_info >= (3, 11) else 1)' >/dev/null 2>&1; then
    PYTHON_CMD="python3"
else
    echo "❌ 需要 Python 3.11+ 才能啟動 AutoTo"
    exit 1
fi

while [ "$#" -gt 0 ]; do
    case "$1" in
        --port)
            if [ "$#" -gt 1 ]; then
                EFFECTIVE_PORT="$2"
                shift
            fi
            ;;
        --port=*)
            EFFECTIVE_PORT="${1#--port=}"
            ;;
    esac
    shift
done

echo "🌐 瀏覽器介面：http://127.0.0.1:${EFFECTIVE_PORT}"
echo ""

cd "$SCRIPT_DIR/backend"
if [ "${#ORIGINAL_ARGS[@]}" -eq 0 ]; then
    exec "$PYTHON_CMD" server.py --port "$AUTOTO_PORT"
fi
exec "$PYTHON_CMD" server.py "${ORIGINAL_ARGS[@]}"
