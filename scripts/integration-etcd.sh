#!/usr/bin/env bash

set -euo pipefail

_repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
_binary="${PEMCAST_BINARY:-${_repo_root}/.local/pemcast-integration}"
_etcd_image="${ETCD_IMAGE:-gcr.io/etcd-development/etcd:v3.7.2}"
_container="pemcast-integration-etcd-$$"
_work_dir="$(mktemp -d)"

__cleanup() {
	docker rm -f "${_container}" >/dev/null 2>&1 || true
}

__main() {
	unset ETCDCTL_USER_AGENT ETCDCTL_USER_PUBLISH
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

	etcdctl user add root:root-integration-password >/dev/null
	etcdctl user grant-role root root >/dev/null
	etcdctl auth enable >/dev/null
	export ETCDCTL_USER=root:root-integration-password
	printf '%s\n' \
		root-integration-password \
		agent-integration-password \
		publisher-integration-password |
		bash "${_repo_root}/.agents/skills/repo-deployment/scripts/init-rbac.sh" \
			--etcd-prefix /pemcast \
			--agent-user agent-integration \
			--publisher-user publisher-integration \
			nginx >/dev/null

	openssl req -x509 -newkey ed25519 -nodes -subj '/CN=integration-a' -days 30 \
		-addext 'basicConstraints=critical,CA:FALSE' \
		-keyout "${_work_dir}/a-key.pem" -out "${_work_dir}/a-cert.pem" >/dev/null 2>&1
	openssl req -x509 -newkey ed25519 -nodes -subj '/CN=integration-b' -days 30 \
		-addext 'basicConstraints=critical,CA:FALSE' \
		-keyout "${_work_dir}/b-key.pem" -out "${_work_dir}/b-cert.pem" >/dev/null 2>&1

	"${_binary}" tools pack \
		--type tls-server \
		--target nginx \
		--certificate "${_work_dir}/a-cert.pem" \
		--private-key "${_work_dir}/a-key.pem" \
		--etcd-prefix /pemcast \
		--output-dir "${_work_dir}/pack-a" >/dev/null
	"${_binary}" tools pack \
		--type tls-server \
		--target nginx \
		--certificate "${_work_dir}/b-cert.pem" \
		--private-key "${_work_dir}/b-key.pem" \
		--etcd-prefix /pemcast \
		--output-dir "${_work_dir}/pack-b" >/dev/null

	_generation_a="$(jq -er .generation "${_work_dir}/pack-a/metadata.json")"
	_generation_b="$(jq -er .generation "${_work_dir}/pack-b/metadata.json")"
	_bundle_key_b="$(jq -er '.["bundle-key"]' "${_work_dir}/pack-b/metadata.json")"
	_active_key="$(jq -er '.["active-key"]' "${_work_dir}/pack-a/metadata.json")"
	_endpoints_json="$(jq -Rn --arg endpoint "${_endpoint}" '[$endpoint]')"

	cat >"${_work_dir}/agent.yaml" <<YAML
agent:
  etcds:
    - name: default
      endpoints: ${_endpoints_json}
      prefix: /pemcast
      user: agent-integration:agent-integration-password
      management-user: publisher-integration:publisher-integration-password
      dial-timeout: 1s
      request-timeout: 2s
  watch:
    progress-interval: 100ms
    progress-timeout: 600ms
  targets:
    - id: nginx
      etcd: default
      type: tls-server
      output:
        root: ${_work_dir}/tls
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

	"${_binary}" --config "${_work_dir}/agent.yaml" publish --pack-dir "${_work_dir}/pack-a"
	test "$(etcdctl get "${_active_key}" --print-value-only)" = "${_generation_a}"

	"${_binary}" --config "${_work_dir}/agent.yaml" agent --once \
		>"${_work_dir}/seeded-agent-once.log" 2>&1
	"${_binary}" --config "${_work_dir}/agent.yaml" status --json \
		>"${_work_dir}/seeded-status.json"
	test "$(jq -er '.targets[0].current.status' "${_work_dir}/seeded-status.json")" = "active"
	test "$(jq -er '.targets[0].current["local-digest"]' "${_work_dir}/seeded-status.json")" = "${_generation_a#sha256-}"

	"${_binary}" --config "${_work_dir}/agent.yaml" publish --pack-dir "${_work_dir}/pack-a"

	_mod_revision_a="$(
		etcdctl get "${_active_key}" -w json | jq -er '.kvs[0].mod_revision'
	)"
	"${_binary}" --config "${_work_dir}/agent.yaml" publish --pack-dir "${_work_dir}/pack-b"
	test "$(etcdctl get "${_active_key}" --print-value-only)" = "${_generation_b}"

	_stale_txn="${_work_dir}/stale.txn"
	_active_json="$(jq -Rrn --arg value "${_active_key}" '$value|@json')"
	_generation_a_json="$(jq -Rrn --arg value "${_generation_a}" '$value|@json')"
	_generation_b_json="$(jq -Rrn --arg value "${_generation_b}" '$value|@json')"
	{
		printf 'val(%s) = %s\n' "${_active_json}" "${_generation_a_json}"
		printf 'mod(%s) = "%s"\n' "${_active_json}" "${_mod_revision_a}"
		printf '\nput %s %s\n\n\n' "${_active_json}" "${_generation_b_json}"
	} >"${_stale_txn}"
	etcdctl txn <"${_stale_txn}" >"${_work_dir}/stale-result.txt"
	test "$(sed -n '1p' "${_work_dir}/stale-result.txt")" = FAILURE

	etcdctl put "${_bundle_key_b}" corrupt >/dev/null
	if "${_binary}" --config "${_work_dir}/agent.yaml" publish --pack-dir "${_work_dir}/pack-b" \
		>"${_work_dir}/mismatch-result.txt" 2>&1; then
		printf '%s\n' 'publish unexpectedly accepted a corrupt existing bundle' >&2
		return 1
	fi
	grep -q 'different bytes' "${_work_dir}/mismatch-result.txt"
	etcdctl del "${_bundle_key_b}" >/dev/null
	"${_binary}" --config "${_work_dir}/agent.yaml" publish --pack-dir "${_work_dir}/pack-b"

	"${_binary}" --config "${_work_dir}/agent.yaml" agent --once --dry-run \
		>"${_work_dir}/agent-dry-run.log" 2>&1
	test "$(readlink "${_work_dir}/tls/current")" = \
		".pemcast/releases/sha256-${_generation_a#sha256-}"

	"${_binary}" --config "${_work_dir}/agent.yaml" agent --once \
		>"${_work_dir}/agent-once.log" 2>&1
	"${_binary}" --config "${_work_dir}/agent.yaml" status --json >"${_work_dir}/status.json"
	test "$(jq -er '.targets[0].current.status' "${_work_dir}/status.json")" = "active"
	test "$(jq -er '.targets[0].current["local-digest"]' "${_work_dir}/status.json")" = "${_generation_b#sha256-}"

	# Watch mode: agent runs continuously and reacts to pointer changes.
	"${_binary}" --config "${_work_dir}/agent.yaml" agent \
		>"${_work_dir}/watch-agent.log" 2>&1 &
	_watch_pid=$!

	# Wait for watch to establish.
	for _i in $(seq 1 30); do
		if grep -q 'certificate bundle activated\|agent started' "${_work_dir}/watch-agent.log" 2>/dev/null; then
			break
		fi
		sleep 0.2
	done

	# Publish pack-a while agent is watching.
	sleep 3
	if grep -q 'invalid auth token' "${_work_dir}/watch-agent.log"; then
		cat "${_work_dir}/watch-agent.log" >&2
		printf '%s\n' 'watch agent logged an expired auth token' >&2
		return 1
	fi
	"${_binary}" --config "${_work_dir}/agent.yaml" publish --pack-dir "${_work_dir}/pack-a" >/dev/null

	# Wait for watch agent to activate pack-a.
	_deadline=$((SECONDS + 15))
	while ((SECONDS < _deadline)); do
		_watch_digest="$("${_binary}" --config "${_work_dir}/agent.yaml" status --json 2>/dev/null | jq -er '.targets[0].current["local-digest"] // empty' 2>/dev/null || true)"
		if [[ "${_watch_digest}" == "${_generation_a#sha256-}" ]]; then
			break
		fi
		sleep 0.3
	done
	kill "${_watch_pid}" 2>/dev/null || true
	wait "${_watch_pid}" 2>/dev/null || true
	test "${_watch_digest}" = "${_generation_a#sha256-}"
	test "$(readlink "${_work_dir}/tls/current")" = \
		".pemcast/releases/sha256-${_generation_a#sha256-}"
	if grep -q 'invalid auth token' "${_work_dir}/watch-agent.log"; then
		cat "${_work_dir}/watch-agent.log" >&2
		printf '%s\n' 'watch agent logged an expired auth token after activation' >&2
		return 1
	fi

	printf '%s\n' "integration ok: nginx ${_generation_b}"
}

__main "$@"
