#!/usr/bin/env bash

set -euo pipefail

_repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
_provided_binary="${PEMCAST_BINARY:-}"
_binary="${_provided_binary:-${_repo_root}/.local/pemcast-upgrade-integration}"
_etcd_image="${ETCD_IMAGE:-gcr.io/etcd-development/etcd:v3.7.2}"
_container="pemcast-upgrade-integration-etcd-$$"
_work_dir="$(mktemp -d)"
_etcd_port=""

__cleanup() {
    docker rm -f "${_container}" >/dev/null 2>&1 || true
}

__require_commands() {
    command -v docker >/dev/null
    command -v etcdctl >/dev/null
    command -v jq >/dev/null
    command -v openssl >/dev/null
    command -v base64 >/dev/null
    command -v sha256sum >/dev/null
}

__wait_etcd() {
    _deadline="$((SECONDS + 15))"
    while ((SECONDS < _deadline)); do
        if etcdctl endpoint health >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.1
    done
    docker logs "${_container}" >&2
    return 1
}

__file_sha256() {
    openssl dgst -sha256 -binary "$1"
}

__v5_generation() {
    {
        printf 'pemcast/v5\0'
        printf 'fullchain.pem\0'
        __file_sha256 "${_work_dir}/fullchain.pem"
        printf 'privkey.pem\0'
        __file_sha256 "${_work_dir}/privkey.pem"
    } | sha256sum | awk '{print $1}'
}

__make_material() {
    openssl req -x509 -newkey ed25519 -nodes -days 2 \
        -subj '/CN=upgrade-integration' \
        -addext 'basicConstraints=critical,CA:FALSE' \
        -addext 'extendedKeyUsage=serverAuth' \
        -keyout "${_work_dir}/privkey.pem" \
        -out "${_work_dir}/fullchain.pem" >/dev/null 2>&1

    _certificate_hash="$(sha256sum "${_work_dir}/fullchain.pem" | awk '{print $1}')"
    _private_key_hash="$(sha256sum "${_work_dir}/privkey.pem" | awk '{print $1}')"
    _certificate_data="$(base64 -w0 "${_work_dir}/fullchain.pem")"
    _private_key_data="$(base64 -w0 "${_work_dir}/privkey.pem")"
    _generation="sha256-$(__v5_generation)"

    jq -n \
        --arg schema "pemcast/v5" \
        --arg certificate_hash "${_certificate_hash}" \
        --arg certificate_data "${_certificate_data}" \
        --arg private_key_hash "${_private_key_hash}" \
        --arg private_key_data "${_private_key_data}" \
        '{
            schema: $schema,
            files: [
                {
                    name: "fullchain.pem",
                    kind: "certificate",
                    sha256: $certificate_hash,
                    encoding: "base64",
                    data: $certificate_data
                },
                {
                    name: "privkey.pem",
                    kind: "private-key",
                    sha256: $private_key_hash,
                    encoding: "base64",
                    data: $private_key_data
                }
            ],
            pairs: [
                {certificate: "fullchain.pem", "private-key": "privkey.pem"}
            ]
        }' >"${_work_dir}/bundle.json"

    etcdctl put "/pemcast/v5/bundles/nginx/${_generation}" \
        "$(cat "${_work_dir}/bundle.json")" >/dev/null
    etcdctl put /pemcast/v5/active/nginx "${_generation}" >/dev/null
}

__enable_auth() {
    etcdctl user add root:root-upgrade-integration-password >/dev/null
    etcdctl user grant-role root root >/dev/null
    etcdctl --user='root:root-upgrade-integration-password' auth enable >/dev/null
}

__init_rbac() {
    printf '%s\n' \
        root-upgrade-integration-password \
        agent-upgrade-integration-password \
        publisher-upgrade-integration-password |
        bash "${_repo_root}/.agents/skills/repo-deployment/scripts/init-rbac.sh" \
            --etcd-prefix /pemcast \
            --broad \
            --agent-user agent-upgrade-integration \
            --publisher-user publisher-upgrade-integration \
            nginx >/dev/null
}

__write_agent_config() {
    cat >"${_work_dir}/agent.yaml" <<YAML
agent:
  state-dir: ${_work_dir}/state
  etcd:
    endpoints: ["http://127.0.0.1:${_etcd_port}"]
    prefix: /pemcast
  targets:
    - id: nginx
      type: tls-server
      delete-policy: retain
      output:
        root: ${_work_dir}/tls
        current-link: current
        retain-releases: 2
        directory-mode: "0700"
        mappings:
          - remote: fullchain.pem
            local: fullchain.pem
            mode: "0644"
          - remote: privkey.pem
            local: privkey.pem
            mode: "0600"
      validation:
        reject-expired: true
        minimum-validity: 1h
      hook:
        path: /bin/true
        args: []
        timeout: 5s
        pass-environment: []
YAML
}

__upgrade() {
    PEMCAST_AGENT_ETCD_ENDPOINTS="[\"http://127.0.0.1:${_etcd_port}\"]" \
        PEMCAST_AGENT_ETCD_PREFIX=/pemcast \
        ETCDCTL_USER_UPGRADE='publisher-upgrade-integration:publisher-upgrade-integration-password' \
        "${_binary}" upgrade "$@"
}

__prefix_count() {
    ETCDCTL_USER='publisher-upgrade-integration:publisher-upgrade-integration-password' \
        etcdctl get "$1" --prefix --write-out=json | jq -er '.count // 0'
}

__main() {
    unset ETCDCTL_USER ETCDCTL_USER_AGENT ETCDCTL_USER_PUBLISH ETCDCTL_USER_UPGRADE
    __require_commands
    if [[ -n "${_provided_binary}" ]]; then
        test -x "${_provided_binary}"
    else
        (cd "${_repo_root}" && go build -o "${_binary}" ./cmd/pemcast)
    fi
    trap __cleanup EXIT HUP INT TERM

    docker rm -f "${_container}" >/dev/null 2>&1 || true
    docker run -d --name "${_container}" -p 127.0.0.1::2379 \
        "${_etcd_image}" etcd \
        --listen-client-urls=http://0.0.0.0:2379 \
        --advertise-client-urls=http://localhost:2379 >/dev/null
    _etcd_port="$(
        docker port "${_container}" 2379/tcp |
            awk '$1 ~ /^127\.0\.0\.1:/ {split($1, _part, ":"); print _part[2]; exit}'
    )"
    test -n "${_etcd_port}"
    export ETCDCTL_ENDPOINTS="http://127.0.0.1:${_etcd_port}"
    __wait_etcd

    __make_material
    __enable_auth
    __init_rbac
    __write_agent_config

    __upgrade --default-type tls-server --dry-run >/dev/null
    test "$(__prefix_count /pemcast/v6/)" == 0

    __upgrade --default-type tls-server >/dev/null
    test "$(
        ETCDCTL_USER='publisher-upgrade-integration:publisher-upgrade-integration-password' \
            etcdctl get /pemcast/v6/active/nginx --print-value-only
    )" != "${_generation}"
    test "$(__prefix_count /pemcast/v5/)" == 2

    ETCDCTL_USER_AGENT='agent-upgrade-integration:agent-upgrade-integration-password' \
        "${_binary}" --config "${_work_dir}/agent.yaml" agent --once --dry-run >/dev/null
    ETCDCTL_USER_AGENT='agent-upgrade-integration:agent-upgrade-integration-password' \
        "${_binary}" --config "${_work_dir}/agent.yaml" agent --once >/dev/null
    test -f "${_work_dir}/tls/current/fullchain.pem"
    test -f "${_work_dir}/tls/current/privkey.pem"

    __upgrade --default-type tls-server --delete-old-v5 --yes >/dev/null
    test "$(__prefix_count /pemcast/v5/)" == 0
    test "$(__prefix_count /pemcast/v6/)" == 2

    printf '%s\n' "upgrade integration ok: nginx ${_generation} -> v6"
}

__main
