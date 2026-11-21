#!/bin/sh

case "$0" in
    /*) launcher=$0 ;;
    *) launcher=./$0 ;;
esac
binary=${launcher%/*}/bin/t3-bridge
if [ ! -f "$binary" ] || [ ! -x "$binary" ]; then
    printf '%s\n' 'Cannot run bin/t3-bridge. Extract the entire desktop ZIP and keep this launcher next to the bin folder.' >&2
    exit 1
fi

exec "$binary" config gui "$@"
