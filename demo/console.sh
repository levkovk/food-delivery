#!/usr/bin/env bash
# Окно терминала на виртуальном экране: команды набираются в xterm,
# окно сохраняется в PNG. Подключается из record.sh.

FONT="${FONT:-Liberation Mono}"
WIN=""
PID=""

open_console() {   # open_console <колонки> <строки>
    xterm -fa "$FONT" -fs 12 -geometry "${1}x${2}+0+0" -bg '#fbfaf6' -fg '#1f1d18' \
        -cr '#2e6b40' +sb -b 12 -T demo -e env "PS1=\\u@k3d-food:\\W\$ " bash --norc &
    PID=$!
    for _ in $(seq 1 50); do
        WIN="$(xdotool search --pid "$PID" 2>/dev/null | tail -1 || true)"
        [[ -n "$WIN" ]] && break
        sleep 0.2
    done
    xdotool mousemove 1 1
    sleep 1.2
}

cmd() {            # cmd <пауза, с> <команда>
    local pause="$1"; shift
    xdotool type --delay 6 -- "$*"
    xdotool key Return
    sleep "$pause"
}

snap() {           # snap <файл>
    import -window "$WIN" "$1"
    convert "$1" -trim +repage -bordercolor '#fbfaf6' -border 14 "$1"
    echo "snapshot: $1"
}

close_console() {
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
    sleep 0.3
}
