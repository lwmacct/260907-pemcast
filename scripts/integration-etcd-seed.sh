#!/usr/bin/env bash

set -euo pipefail

_repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
_provided_binary="${PEMCAST_BINARY:-}"
_binary="${_provided_binary:-${_repo_root}/.local/pemcast-seed-integration}"
_etcd_image="${ETCD_IMAGE:-gcr.io/etcd-development/etcd:v3.7.2}"
_container="pemcast-seed-integration-etcd-$$"
_copy_container="pemcast-seed-integration-copy-$$"
_tls_volume="pemcast-seed-integration-tls-$$"
_work_dir="$(mktemp -d)"
_agent_pid=""

__cleanup() {
    if [[ -n "${_agent_pid}" ]] && kill -0 "${_agent_pid}" 2>/dev/null; then
        kill "${_agent_pid}" 2>/dev/null || true
        wait "${_agent_pid}" 2>/dev/null || true
    fi
    docker rm -f "${_container}" >/dev/null 2>&1 || true
    docker rm -f "${_copy_container}" >/dev/null 2>&1 || true
    docker volume rm "${_tls_volume}" >/dev/null 2>&1 || true
}

__make_ca() {
    openssl req -x509 -newkey ed25519 -nodes -days 2 \
        -subj '/CN=pemcast seed integration CA' \
        -addext 'basicConstraints=critical,CA:TRUE' \
        -addext 'keyUsage=critical,keyCertSign,cRLSign' \
        -keyout "${_work_dir}/ca.key" -out "${_work_dir}/ca.pem" \
        >/dev/null 2>&1
}

__make_leaf() {
    _name="$1"
    openssl req -new -newkey ed25519 -nodes \
        -subj '/CN=localhost' \
        -keyout "${_work_dir}/${_name}.key" \
        -out "${_work_dir}/${_name}.csr" >/dev/null 2>&1
    printf '%s\n' \
        'basicConstraints=critical,CA:FALSE' \
        'keyUsage=critical,digitalSignature,keyEncipherment' \
        'extendedKeyUsage=serverAuth' \
        'subjectAltName=DNS:localhost,IP:127.0.0.1' \
        >"${_work_dir}/${_name}.ext"
    openssl x509 -req -in "${_work_dir}/${_name}.csr" \
        -CA "${_work_dir}/ca.pem" -CAkey "${_work_dir}/ca.key" -CAcreateserial \
        -days 2 -sha256 -extfile "${_work_dir}/${_name}.ext" \
        -out "${_work_dir}/${_name}.pem" >/dev/null 2>&1
    cat "${_work_dir}/${_name}.pem" "${_work_dir}/ca.pem" \
        >"${_work_dir}/${_name}-fullchain.pem"
}

__pack() {
    _name="$1"
    "${_binary}" tools pack \
        --type tls-server \
        --target nginx \
        --etcd-prefix /pemcast \
        --certificate "${_work_dir}/${_name}-fullchain.pem" \
        --private-key "${_work_dir}/${_name}.key" \
        --output-dir "${_work_dir}/pack-${_name}" >/dev/null
}

__serial() {
    _name="$1"
    openssl x509 -in "${_work_dir}/${_name}.pem" -noout -serial | cut -d= -f2
}

__served_serial() {
    printf '' |
        openssl s_client -connect "localhost:${_etcd_port}" -servername localhost \
            -CAfile "${_work_dir}/ca.pem" -verify_return_error 2>/dev/null |
        openssl x509 -noout -serial |
        cut -d= -f2
}

__wait_file_value() {
    _path="$1"
    _expected="$2"
    _deadline="$((SECONDS + 10))"
    while ((SECONDS < _deadline)); do
        if [[ -f "${_path}" ]] && [[ "$(jq -er '.targets[0].state.generation' "${_path}")" == "${_expected}" ]]; then
            return 0
        fi
        "${_binary}" --config "${_work_dir}/agent.yaml" status --json >"${_path}" 2>/dev/null || true
        sleep 0.1
    done
    printf '%s\n' 'timed out waiting for agent generation' >&2
    return 1
}

__stop_agent() {
    if [[ -n "${_agent_pid}" ]] && kill -0 "${_agent_pid}" 2>/dev/null; then
        kill "${_agent_pid}"
        wait "${_agent_pid}" 2>/dev/null || true
    fi
    _agent_pid=""
}

__sync_tls_volume() {
    docker cp "${_work_dir}/tls/." "${_copy_container}:/tls/"
}

__publish() {
    _name="$1"
    PEMCAST_AGENT_ETCD_ENDPOINTS="[\"https://localhost:${_etcd_port}\"]" \
        PEMCAST_AGENT_ETCD_TLS_CA_FILE="${_work_dir}/ca.pem" \
        PEMCAST_AGENT_ETCD_TLS_SERVER_NAME=localhost \
        PEMCAST_AGENT_ETCD_PREFIX=/pemcast \
        ETCDCTL_USER_PUBLISH='publisher-integration:publisher-integration-password' \
        "${_binary}" publish --pack-dir "${_work_dir}/pack-${_name}" >/dev/null
}

__start_agent() {
    PEMCAST_AGENT_ETCD_ENDPOINTS="[\"https://localhost:${_etcd_port}\"]" \
        PEMCAST_AGENT_ETCD_TLS_CA_FILE="${_work_dir}/ca.pem" \
        PEMCAST_AGENT_ETCD_TLS_SERVER_NAME=localhost \
        PEMCAST_AGENT_ETCD_PREFIX=/pemcast \
        ETCDCTL_USER_AGENT='agent-integration:agent-integration-password' \
        "${_binary}" --config "${_work_dir}/agent.yaml" agent \
        >"${_work_dir}/agent.log" 2>&1 &
    _agent_pid=$!
}

__main() {
    unset ETCDCTL_USER ETCDCTL_USER_AGENT ETCDCTL_USER_PUBLISH
    if [[ -n "${_provided_binary}" ]]; then
        test -x "${_provided_binary}"
    else
        (cd "${_repo_root}" && go build -o "${_binary}" ./cmd/pemcast)
    fi
    trap __cleanup EXIT HUP INT TERM

    __make_ca
    for _name in a b rescue; do
        __make_leaf "${_name}"
        __pack "${_name}"
    done
    _generation_a="$(jq -er .generation "${_work_dir}/pack-a/metadata.json")"
    _generation_b="$(jq -er .generation "${_work_dir}/pack-b/metadata.json")"
    _generation_rescue="$(jq -er .generation "${_work_dir}/pack-rescue/metadata.json")"

    cat >"${_work_dir}/agent.yaml" <<YAML
agent:
  state-dir: ${_work_dir}/state
  etcd:
    endpoints: ["https://integration-invalid:2379"]
    prefix: /pemcast
    tls:
      ca-file: ${_work_dir}/ca.pem
      server-name: localhost
  targets:
    - id: nginx
      type: tls-server
      delete-policy: retain
      output:
        root: ${_work_dir}/tls
        current-link: current
        retain-releases: 3
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

    "${_binary}" --config "${_work_dir}/agent.yaml" \
        tools \
        seed --target nginx --pack-dir "${_work_dir}/pack-a" >/dev/null
    test "$(readlink "${_work_dir}/tls/current")" = \
        ".pemcast/releases/sha256-${_generation_a#sha256-}"

    docker rm -f "${_container}" >/dev/null 2>&1 || true
    docker volume create "${_tls_volume}" >/dev/null
    docker run -d --name "${_copy_container}" \
        --mount "type=volume,src=${_tls_volume},dst=/tls" \
        alpine:3.20 sleep 300 >/dev/null
    docker cp "${_work_dir}/tls/." "${_copy_container}:/tls/"
    docker run -d --name "${_container}" \
        -p 127.0.0.1::2379 \
        --mount "type=volume,src=${_tls_volume},dst=/tls,readonly" \
        "${_etcd_image}" \
        etcd \
        --listen-client-urls=https://0.0.0.0:2379 \
        --advertise-client-urls=https://localhost:2379 \
        --cert-file=/tls/current/fullchain.pem \
        --key-file=/tls/current/privkey.pem >/dev/null
    _etcd_port="$(
        docker port "${_container}" 2379/tcp |
            awk '$1 ~ /^127\.0\.0\.1:/ {split($1, _part, ":"); print _part[2]; exit}'
    )"
    test -n "${_etcd_port}"

    _deadline="$((SECONDS + 10))"
    until ETCDCTL_ENDPOINTS="https://localhost:${_etcd_port}" \
        ETCDCTL_CACERT="${_work_dir}/ca.pem" \
        etcdctl endpoint health >/dev/null 2>&1; do
        if ((SECONDS >= _deadline)); then
            docker logs "${_container}" >&2
            return 1
        fi
        sleep 0.1
    done
    test "$(__served_serial)" == "$(__serial a)"
    _etcd_pid_before="$(docker inspect -f '{{.State.Pid}}' "${_container}")"

    export ETCDCTL_ENDPOINTS="https://localhost:${_etcd_port}"
    export ETCDCTL_CACERT="${_work_dir}/ca.pem"
    etcdctl user add root:root-integration-password >/dev/null
    etcdctl user grant-role root root >/dev/null
    etcdctl --user='root:root-integration-password' auth enable >/dev/null
    printf '%s\n' \
        root-integration-password \
        agent-integration-password \
        publisher-integration-password |
        bash "${_repo_root}/.agents/skills/repo-deployment/scripts/init-rbac.sh" \
            --etcd-prefix /pemcast \
            --agent-user agent-integration \
            --publisher-user publisher-integration \
            nginx >/dev/null

    __start_agent
    __publish a
    __wait_file_value "${_work_dir}/status-a.json" "${_generation_a}"
    test "$(__served_serial)" == "$(__serial a)"

    __publish b
    __wait_file_value "${_work_dir}/status-b.json" "${_generation_b}"
    __sync_tls_volume
    test "$(__served_serial)" == "$(__serial b)"

    __stop_agent
    "${_binary}" --config "${_work_dir}/agent.yaml" \
        tools \
        seed --force --target nginx --pack-dir "${_work_dir}/pack-rescue" >/dev/null
    __sync_tls_volume
    test "$(__served_serial)" == "$(__serial rescue)"
    __publish rescue
    __start_agent
    __wait_file_value "${_work_dir}/status-rescue.json" "${_generation_rescue}"
    test "$(__served_serial)" == "$(__serial rescue)"

    _etcd_pid_after="$(docker inspect -f '{{.State.Pid}}' "${_container}")"
    test "${_etcd_pid_before}" == "${_etcd_pid_after}"
    printf '%s\n' "seed integration ok: nginx ${_generation_rescue}"
}

__main
