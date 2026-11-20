#!/usr/bin/env bash

set -euo pipefail

_repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
_binary="${PEMCAST_BINARY:-${_repo_root}/.local/pemcast-multi-source-integration}"
_etcd_image="${ETCD_IMAGE:-gcr.io/etcd-development/etcd:v3.7.2}"
_container="pemcast-multi-source-etcd-$$"
_work_dir="$(mktemp -d)"
_agent_pid=""

__cleanup() {
	if [[ -n "${_agent_pid}" ]]; then
		kill "${_agent_pid}" >/dev/null 2>&1 || true
		wait "${_agent_pid}" >/dev/null 2>&1 || true
	fi
	docker rm -f "${_container}" >/dev/null 2>&1 || true
	rm -rf "${_work_dir}"
}

__main() {
	(cd "${_repo_root}" && go build -o "${_binary}" ./cmd/pemcast)

	docker rm -f "${_container}" >/dev/null 2>&1 || true
	docker run -d --name "${_container}" \
		-p 127.0.0.1::2379 \
		"${_etcd_image}" \
		etcd \
		--listen-client-urls=http://0.0.0.0:2379 \
		--advertise-client-urls=http://127.0.0.1:2379 \
		--auth-token-ttl=1 \
		>/dev/null
	trap __cleanup EXIT HUP INT TERM

	_etcd_port="$(
		docker port "${_container}" 2379/tcp |
			awk '$1 ~ /^127\.0\.0\.1:/ {split($1, _part, ":"); print _part[2]; exit}'
	)"
	test -n "${_etcd_port}"
	_endpoint="http://127.0.0.1:${_etcd_port}"

	export ETCDCTL_ENDPOINTS="${_endpoint}"
	etcdctl endpoint status >/dev/null
	etcdctl user add root:root-multi-source >/dev/null
	etcdctl user grant-role root root >/dev/null
	etcdctl auth enable >/dev/null
	export ETCDCTL_USER=root:root-multi-source

	printf '%s\n' \
		root-multi-source \
		agent-bendian-password \
		publisher-bendian-password |
		bash "${_repo_root}/.agents/skills/repo-deployment/scripts/init-rbac.sh" \
			--etcd-prefix /pemcast/tenants/bendian \
			--agent-user agent-bendian \
			--publisher-user publisher-bendian \
			shared >/dev/null
	printf '%s\n' \
		root-multi-source \
		agent-lwmacct-password \
		publisher-lwmacct-password |
		bash "${_repo_root}/.agents/skills/repo-deployment/scripts/init-rbac.sh" \
			--etcd-prefix /pemcast/tenants/lwmacct \
			--agent-user agent-lwmacct \
			--publisher-user publisher-lwmacct \
			shared >/dev/null

	openssl req -x509 -newkey ed25519 -nodes -subj '/CN=bendian.example' -days 30 \
		-addext 'basicConstraints=critical,CA:FALSE' \
		-keyout "${_work_dir}/bendian-key.pem" \
		-out "${_work_dir}/bendian-cert.pem" >/dev/null 2>&1
	openssl req -x509 -newkey ed25519 -nodes -subj '/CN=lwmacct.example' -days 30 \
		-addext 'basicConstraints=critical,CA:FALSE' \
		-keyout "${_work_dir}/lwmacct-key.pem" \
		-out "${_work_dir}/lwmacct-cert.pem" >/dev/null 2>&1

	"${_binary}" tools pack \
		--type tls-server --target shared \
		--certificate "${_work_dir}/bendian-cert.pem" \
		--private-key "${_work_dir}/bendian-key.pem" \
		--etcd-prefix /pemcast/tenants/bendian \
		--output-dir "${_work_dir}/pack-bendian" >/dev/null
	"${_binary}" tools pack \
		--type tls-server --target shared \
		--certificate "${_work_dir}/lwmacct-cert.pem" \
		--private-key "${_work_dir}/lwmacct-key.pem" \
		--etcd-prefix /pemcast/tenants/lwmacct \
		--output-dir "${_work_dir}/pack-lwmacct" >/dev/null

	_generation_bendian="$(jq -er .generation "${_work_dir}/pack-bendian/metadata.json")"
	_generation_lwmacct="$(jq -er .generation "${_work_dir}/pack-lwmacct/metadata.json")"

	cat >"${_work_dir}/agent.yaml" <<YAML
agent:
  etcds:
    - name: bendian
      endpoints: ${_endpoint}
      prefix: /pemcast/tenants/bendian
      user: agent-bendian:agent-bendian-password
      management-user: publisher-bendian:publisher-bendian-password
      dial-timeout: 1s
      request-timeout: 2s
    - name: lwmacct
      endpoints: ${_endpoint}
      prefix: /pemcast/tenants/lwmacct
      user: agent-lwmacct:agent-lwmacct-password
      management-user: publisher-lwmacct:publisher-lwmacct-password
      dial-timeout: 1s
      request-timeout: 2s
  watch:
    progress-interval: 100ms
    progress-timeout: 600ms
  targets:
    - id: shared
      etcd: bendian
      type: tls-server
      output:
        root: ${_work_dir}/bendian
      hook:
        path: /bin/true
        timeout: 2s
    - id: shared
      etcd: lwmacct
      type: tls-server
      output:
        root: ${_work_dir}/lwmacct
      hook:
        path: /bin/true
        timeout: 2s
YAML

	"${_binary}" --config "${_work_dir}/agent.yaml" config validate
	"${_binary}" --config "${_work_dir}/agent.yaml" agent \
		>"${_work_dir}/agent.log" 2>&1 &
	_agent_pid=$!
	sleep 1

	"${_binary}" --config "${_work_dir}/agent.yaml" publish \
		--etcd-name bendian --pack-dir "${_work_dir}/pack-bendian" >/dev/null

	"${_binary}" --config "${_work_dir}/agent.yaml" publish \
		--etcd-name lwmacct --pack-dir "${_work_dir}/pack-lwmacct" >/dev/null

	_deadline=$((SECONDS + 15))
	while ((SECONDS < _deadline)); do
		_status="$("${_binary}" --config "${_work_dir}/agent.yaml" status --json 2>/dev/null || true)"
		_bendian_digest="$(jq -er '.targets[0].current["local-digest"] // empty' <<<"${_status}" 2>/dev/null || true)"
		_lwmacct_digest="$(jq -er '.targets[1].current["local-digest"] // empty' <<<"${_status}" 2>/dev/null || true)"
		if [[ "${_bendian_digest}" == "${_generation_bendian#sha256-}" &&
			"${_lwmacct_digest}" == "${_generation_lwmacct#sha256-}" ]]; then
			break
		fi
		sleep 0.2
	done

	test "${_bendian_digest}" = "${_generation_bendian#sha256-}"
	test "${_lwmacct_digest}" = "${_generation_lwmacct#sha256-}"
	if grep -q 'invalid auth token' "${_work_dir}/agent.log"; then
		cat "${_work_dir}/agent.log" >&2
		printf '%s\n' 'multi-source agent logged an invalid auth token' >&2
		return 1
	fi

	printf '%s\n' "multi-source integration ok: ${_generation_bendian} ${_generation_lwmacct}"
}

__main "$@"
