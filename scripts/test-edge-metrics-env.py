#!/usr/bin/env python3
"""Run the installers' actual listener-setting preservation blocks without root."""
import os
from pathlib import Path
import re
import subprocess
import tempfile

root = Path(__file__).resolve().parents[1]

# Every listener the Edge binary understands must be preserved by both installers.
ports_go = (root / "internal/edgeagent/config/ports.go").read_text()
go_keys = sorted(set(re.findall(r'Env: "(ONGRID_EDGE_[A-Z_]+_ADDR)"', ports_go)))
assert go_keys, "no listener env keys found in ports.go"

MARK = '# Preserve only the local listener settings;'
APPEND = 'if [[ -n "$LISTENER_ENV_LINES" ]]; then'

for name in ("install.sh", "install-edge.sh"):
    source = (root / "deploy/install/edge" / name).read_text()
    capture = source.split(MARK, 1)[1].split('\ndone\n', 1)[0]
    capture = capture.split('\n', 1)[1] + '\ndone\n'
    keys = sorted(re.search(r'LISTENER_ENV_KEYS="([^"]*)"', capture).group(1).split())
    assert keys == go_keys, (name, "installer keys out of sync with ports.go", set(go_keys) ^ set(keys))
    append = APPEND + source.split(APPEND, 1)[1].split('\nfi\n', 1)[0] + '\nfi\n'
    script = 'log_error() { printf "%s\\n" "$*" >&2; }\n' + capture
    script += 'printf "NEW_CREDENTIALS=placeholder\\n" > "$ENV_FILE"\n' + append

    for key in ("ONGRID_EDGE_METRICS_ADDR", "ONGRID_EDGE_HOSTMETRICS_ADDR", "ONGRID_EDGE_TRACES_HTTP_ADDR"):
        for old, new, expected in (
            (None, None, None),
            (None, ':19101', ':19101'),
            (':19101', None, ':19101'),
            ('"[::1]:19101"', None, '"[::1]:19101"'),
            (':19101', ':29101', ':29101'),
            (':19101', '', ''),
            (':19101', ':29101\nINJECTED=value', 'reject'),
        ):
            with tempfile.TemporaryDirectory() as tmp:
                env_file = Path(tmp) / 'edge.env'
                if old is not None:
                    env_file.write_text('OLD_CREDENTIALS=placeholder\nONGRID_EDGE_CLOUD_ADDR=old:40012\n' + key + '=' + old + '\n')
                env = dict(os.environ, ENV_FILE=str(env_file))
                for k in go_keys:
                    env.pop(k, None)
                if new is not None:
                    env[key] = new
                result = subprocess.run(['bash', '-eu', '-c', script], env=env, capture_output=True, text=True)
                if expected == 'reject':
                    assert result.returncode != 0, (name, key, 'accepted newline')
                    assert env_file.read_text().startswith('OLD_CREDENTIALS='), (name, key)
                    continue
                assert result.returncode == 0, (name, key, result.stderr)
                lines = env_file.read_text().splitlines()
                actual = [line for line in lines if line.startswith(key + '=')]
                want = [] if expected is None else [key + '=' + expected]
                assert actual == want, (name, key, actual, want)
                assert 'OLD_CREDENTIALS=placeholder' not in lines, (name, key)
                assert not any(line.startswith('ONGRID_EDGE_CLOUD_ADDR=') for line in lines), (name, key, 'cloud addr must not be preserved')

    # Several overrides survive a reinstall together and keep their order.
    with tempfile.TemporaryDirectory() as tmp:
        env_file = Path(tmp) / 'edge.env'
        env_file.write_text('OLD=1\nONGRID_EDGE_HOSTMETRICS_ADDR=:19102\nONGRID_EDGE_PROCMETRICS_ADDR=:19256\n')
        env = dict(os.environ, ENV_FILE=str(env_file), ONGRID_EDGE_PPROF_ADDR='off')
        for k in go_keys:
            if k != 'ONGRID_EDGE_PPROF_ADDR':
                env.pop(k, None)
        result = subprocess.run(['bash', '-eu', '-c', script], env=env, capture_output=True, text=True)
        assert result.returncode == 0, (name, result.stderr)
        kept = [l for l in env_file.read_text().splitlines() if l.startswith('ONGRID_EDGE_')]
        assert kept == ['ONGRID_EDGE_PPROF_ADDR=off', 'ONGRID_EDGE_HOSTMETRICS_ADDR=:19102', 'ONGRID_EDGE_PROCMETRICS_ADDR=:19256'], (name, kept)
    print(name + ': listener address install/reinstall checks passed')
