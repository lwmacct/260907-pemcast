#!/usr/bin/env bash

set -euo pipefail
if [[ "${PEMCAST_INTEGRATION_TRACE:-0}" == "1" ]]; then
    set -x
fi

_repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
_provided_binary="${PEMCAST_BINARY:-}"
_binary="${_provided_binary:-${_repo_root}/.local/pemcast-prune-integration}"
_etcd_image="${ETCD_IMAGE:-gcr.io/etcd-development/etcd:v3.7.2}"
_container="pemcast-prune-integration-etcd-$$"
_work_dir="$(mktemp -d)"
_etcd_port=""

__cleanup() {
    docker rm -f "${_container}" >/dev/null 2>&1 || true
    if [[ -d "${_work_dir}" ]]; then
        find "${_work_dir}" -type f -exec shred -u {} +
        find "${_work_dir}" -depth -type d -empty -delete
    fi
}

__require_commands() {
    command -v docker >/dev/null
    command -v etcdctl >/dev/null
    command -v jq >/dev/null
    command -v openssl >/dev/null
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

__make_identity() {
    _name="$1"
    _not_before="$2"
    _not_after="$3"

    openssl req -new -newkey ed25519 -nodes \
        -subj "/CN=${_name}" \
        -keyout "${_work_dir}/${_name}.key" \
        -out "${_work_dir}/${_name}.csr" >/dev/null 2>&1
    openssl ca -batch -selfsign \
        -in "${_work_dir}/${_name}.csr" \
        -keyfile "${_work_dir}/${_name}.key" \
        -startdate "${_not_before}" \
        -enddate "${_not_after}" \
        -extfile "${_work_dir}/identity.ext" \
        -config "${_work_dir}/ca.cnf" \
        -out "${_work_dir}/${_name}.pem" >/dev/null 2>&1
}

__make_expired_ca() {
    openssl req -new -newkey ed25519 -nodes \
        -subj '/CN=prune-expired-ca' \
        -keyout "${_work_dir}/trust.key" \
        -out "${_work_dir}/trust.csr" >/dev/null 2>&1
    openssl ca -batch -selfsign \
        -in "${_work_dir}/trust.csr" \
        -keyfile "${_work_dir}/trust.key" \
        -startdate 200102000000Z \
        -enddate 200103000000Z \
        -extfile "${_work_dir}/trust.ext" \
        -config "${_work_dir}/ca.cnf" \
        -out "${_work_dir}/trust.pem" >/dev/null 2>&1
}

__make_active_ca() {
    openssl req -x509 -newkey ed25519 -nodes -days 2 \
        -subj '/CN=prune-active-ca' \
        -addext 'basicConstraints=critical,CA:TRUE' \
        -addext 'keyUsage=critical,keyCertSign,cRLSign' \
        -keyout "${_work_dir}/active-trust.key" \
        -out "${_work_dir}/active-trust.pem" >/dev/null 2>&1
}

__pack_identity() {
    _name="$1"
    "${_binary}" tools pack \
        --type tls-server \
        --target nginx \
        --etcd-prefix /pemcast \
        --certificate "${_work_dir}/${_name}.pem" \
        --private-key "${_work_dir}/${_name}.key" \
        --output-dir "${_work_dir}/pack-${_name}" >/dev/null
}

__generation() {
    _directory="$1"
    jq -er .generation "${_directory}/metadata.json"
}

__stage_pack() {
    _directory="$1"
    etcdctl txn <"${_directory}/stage.txn" >/dev/null
}

__key_count() {
    etcdctl get "$1" --prefix --write-out=json | jq -er '.count // 0'
}

__revision() {
    etcdctl endpoint status --write-out=json | jq -er '.[0].Status.header.revision'
}

__prune() {
    PEMCAST_AGENT_ETCD_ENDPOINTS="[\"http://127.0.0.1:${_etcd_port}\"]" \
        PEMCAST_AGENT_ETCD_PREFIX=/pemcast \
        "${_binary}" prune "$@"
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

__main() {
    unset ETCDCTL_USER ETCDCTL_USER_AGENT ETCDCTL_USER_PUBLISH ETCDCTL_USER_UPGRADE ETCDCTL_USER_PRUNE
    __require_commands
    if [[ -n "${_provided_binary}" ]]; then
        test -x "${_provided_binary}"
    else
        (cd "${_repo_root}" && go build -o "${_binary}" ./cmd/pemcast)
    fi
    trap __cleanup EXIT HUP INT TERM

    cat >"${_work_dir}/identity.ext" <<'EOF'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
EOF
    cat >"${_work_dir}/trust.ext" <<'EOF'
basicConstraints=critical,CA:TRUE
keyUsage=critical,keyCertSign,cRLSign
EOF
    mkdir "${_work_dir}/newcerts"
    touch "${_work_dir}/index.txt"
    echo 1000 >"${_work_dir}/serial"
    cat >"${_work_dir}/ca.cnf" <<EOF
[ ca ]
default_ca = CA_default

[ CA_default ]
dir = ${_work_dir}
database = ${_work_dir}/index.txt
serial = ${_work_dir}/serial
new_certs_dir = ${_work_dir}/newcerts
unique_subject = no
default_md = default
policy = policy_any

[ policy_any ]
commonName = optional
EOF

    openssl req -x509 -newkey ed25519 -nodes -days 2 \
        -subj '/CN=prune-active' \
        -addext 'basicConstraints=critical,CA:FALSE' \
        -addext 'extendedKeyUsage=serverAuth' \
        -keyout "${_work_dir}/active.key" \
        -out "${_work_dir}/active.pem" >/dev/null 2>&1
    __make_identity expired 200102000000Z 200103000000Z
    __make_expired_ca
    __make_active_ca

    __pack_identity active
    __pack_identity expired
    "${_binary}" tools pack \
        --type trust \
        --target internal-ca \
        --etcd-prefix /pemcast \
        --ca "${_work_dir}/trust.pem" \
        --output-dir "${_work_dir}/pack-trust" >/dev/null
    "${_binary}" tools pack \
        --type trust \
        --target internal-ca \
        --etcd-prefix /pemcast \
        --ca "${_work_dir}/active-trust.pem" \
        --output-dir "${_work_dir}/pack-active-trust" >/dev/null

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
    __write_agent_config

    _active_generation="$(__generation "${_work_dir}/pack-active")"
    _expired_generation="$(__generation "${_work_dir}/pack-expired")"
    _trust_generation="$(__generation "${_work_dir}/pack-trust")"
    _active_trust_generation="$(__generation "${_work_dir}/pack-active-trust")"
    __stage_pack "${_work_dir}/pack-active"
    __stage_pack "${_work_dir}/pack-expired"
    __stage_pack "${_work_dir}/pack-trust"
    __stage_pack "${_work_dir}/pack-active-trust"
    etcdctl put /pemcast/v6/active/nginx "${_active_generation}" >/dev/null
    etcdctl put /pemcast/v6/active/internal-ca "${_active_trust_generation}" >/dev/null

    _revision_before="$(__revision)"
    __prune --retention 720h --dry-run >"${_work_dir}/prune-dry-run.txt"
    _revision_after="$(__revision)"
    test "${_revision_before}" == "${_revision_after}"
    grep -F "scanned=4 eligible=1 deleted=0" "${_work_dir}/prune-dry-run.txt" >/dev/null
    grep -F "${_expired_generation}" "${_work_dir}/prune-dry-run.txt" | grep -F "would-delete" >/dev/null
    grep -F "${_active_generation}" "${_work_dir}/prune-dry-run.txt" | grep -F "active" >/dev/null
    grep -F "${_trust_generation}" "${_work_dir}/prune-dry-run.txt" | grep -F "trust-skipped" >/dev/null
    test "$(__key_count /pemcast/v6/)" == 6

    __prune --retention 720h --delete >"${_work_dir}/prune-delete.txt"
    grep -F "scanned=4 eligible=1 deleted=1" "${_work_dir}/prune-delete.txt" >/dev/null
    test "$(__key_count /pemcast/v6/)" == 5
    test "$(etcdctl get /pemcast/v6/active/nginx --print-value-only)" == "${_active_generation}"
    test "$(etcdctl get /pemcast/v6/active/internal-ca --print-value-only)" == "${_active_trust_generation}"

    "${_binary}" --config "${_work_dir}/agent.yaml" agent --once --dry-run >/dev/null
    "${_binary}" --config "${_work_dir}/agent.yaml" agent --once >/dev/null
    test -f "${_work_dir}/tls/current/fullchain.pem"
    test -f "${_work_dir}/tls/current/privkey.pem"

    __prune --retention 720h --dry-run >"${_work_dir}/prune-again.txt"
    grep -F "scanned=3 eligible=0 deleted=0" "${_work_dir}/prune-again.txt" >/dev/null

    printf '%s\n' "prune integration ok: active=${_active_generation} deleted=${_expired_generation}"
}

__main
