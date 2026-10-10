#!/usr/bin/env bash

# Manual pemcast v5 publisher for operators who intentionally use etcdctl.
# The supported product path is `pemcast tools pack` followed by `pemcast publish`.

set -euo pipefail

_ETCDCTL="${ETCDCTL:-etcdctl}"
_PACK_DIR=""

__usage() {
  printf '%s\n' 'Usage: publish-v5.sh --pack-dir DIR'
  printf '%s\n' ''
  printf '%s\n' 'Publish a pemcast v5 pack with etcdctl.'
  printf '%s\n' ''
  printf '%s\n' 'Required:'
  printf '%s\n' '  --pack-dir DIR  directory produced by pemcast tools pack'
  printf '%s\n' ''
  printf '%s\n' 'etcdctl connection settings are read from ETCDCTL_ENDPOINTS and standard'
  printf '%s\n' 'ETCDCTL_* environment variables.'
}

__json_quote() {
  _json_quote_value="$1"
  jq -Rrn --arg value "${_json_quote_value}" '$value | @json'
}

__read_metadata() {
  _metadata_file="${_PACK_DIR}/metadata.json"

  _metadata_schema="$(jq -er '.schema' "${_metadata_file}")"
  if [[ "${_metadata_schema}" != 'pemcast-pack/v5' ]]; then
    printf '%s\n' 'unsupported pack metadata schema' >&2
    return 1
  fi

  _etcd_prefix="$(jq -er '.["etcd-prefix"]' "${_metadata_file}")"
  _target_id="$(jq -er '.["target-id"]' "${_metadata_file}")"
  _generation="$(jq -er '.generation' "${_metadata_file}")"
  _active_key="$(jq -er '.["active-key"]' "${_metadata_file}")"
  _bundle_key="$(jq -er '.["bundle-key"]' "${_metadata_file}")"
  _bundle_value_sha256="$(jq -er '.["bundle-value-sha256"]' "${_metadata_file}")"

  if [[ "${_etcd_prefix}" == '/' ]]; then
    _protocol_root='/v5'
  else
    _protocol_root="${_etcd_prefix}/v5"
  fi
  _expected_active_key="${_protocol_root}/active/${_target_id}"
  _expected_bundle_key="${_protocol_root}/bundles/${_target_id}/${_generation}"

  if [[ "${_active_key}" != "${_expected_active_key}" || "${_bundle_key}" != "${_expected_bundle_key}" ]]; then
    printf '%s\n' 'pack metadata keys do not match its prefix, target, and generation' >&2
    return 1
  fi
  if ! [[ "${_target_id}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]; then
    printf '%s\n' 'pack target id is unsafe' >&2
    return 1
  fi
  if ! [[ "${_generation}" =~ ^sha256-[0-9a-f]{64}$ ]]; then
    printf '%s\n' 'pack generation is unsafe' >&2
    return 1
  fi
}

__stage_bundle() {
  _bundle_file="${_PACK_DIR}/bundle.json"
  _expected_stage_txn="${_PACK_DIR}/stage.txn"
  _generated_stage_txn="${_WORK_DIR}/stage.txn"
  _stage_output="${_WORK_DIR}/stage-output.txt"
  _remote_bundle_json="${_WORK_DIR}/remote-bundle.json"
  _remote_bundle="${_WORK_DIR}/remote-bundle.json.value"

  test -r "${_bundle_file}"
  test -r "${_expected_stage_txn}"

  _bundle_key_json="$(__json_quote "${_bundle_key}")"
  _bundle_value_json="$(jq -Rs . "${_bundle_file}")"
  printf 'create(%s) = "0"\n\nput %s %s\n\n\n' \
    "${_bundle_key_json}" \
    "${_bundle_key_json}" \
    "${_bundle_value_json}" >"${_generated_stage_txn}"
  if ! cmp -s "${_expected_stage_txn}" "${_generated_stage_txn}"; then
    printf '%s\n' 'pack stage transaction does not match bundle.json' >&2
    return 1
  fi
  if [[ "$(sha256sum "${_bundle_file}" | awk '{print $1}')" != "${_bundle_value_sha256}" ]]; then
    printf '%s\n' 'pack bundle bytes do not match metadata' >&2
    return 1
  fi

  "${_ETCDCTL}" txn <"${_generated_stage_txn}" >"${_stage_output}"
  if [[ "$(sed -n '1p' "${_stage_output}")" == 'SUCCESS' ]]; then
    return 0
  fi

  "${_ETCDCTL}" get "${_bundle_key}" --write-out=json >"${_remote_bundle_json}"
  if [[ "$(jq -er '.count // 0' "${_remote_bundle_json}")" != '1' ]]; then
    printf '%s\n' 'existing bundle is missing after failed staging transaction' >&2
    return 1
  fi
  jq -er '.kvs[0].value' "${_remote_bundle_json}" | base64 -d >"${_remote_bundle}"
  if ! cmp -s "${_bundle_file}" "${_remote_bundle}"; then
    printf '%s\n' 'existing content-addressed bundle has different bytes' >&2
    return 1
  fi
}

__capture_active() {
  _active_json="${_WORK_DIR}/active.json"

  "${_ETCDCTL}" get "${_active_key}" --write-out=json >"${_active_json}"
  _active_count="$(jq -er '.count // 0' "${_active_json}")"
  if [[ "${_active_count}" == '0' ]]; then
    _active_exists=false
    _active_generation=''
    _active_mod_revision=0
    return 0
  fi
  if [[ "${_active_count}" != '1' ]]; then
    printf '%s\n' 'active pointer get returned more than one key' >&2
    return 1
  fi

  _active_exists=true
  _active_mod_revision="$(jq -er '.kvs[0].mod_revision' "${_active_json}")"
  _active_generation="$(jq -er '.kvs[0].value' "${_active_json}" | base64 -d)"
  if ! [[ "${_active_generation}" =~ ^sha256-[0-9a-f]{64}$ ]]; then
    printf '%s\n' 'remote active pointer is unsafe' >&2
    return 1
  fi
}

__commit_pointer() {
  _pointer_txn="${_WORK_DIR}/pointer.txn"
  _pointer_output="${_WORK_DIR}/pointer-output.txt"
  _active_key_json="$(__json_quote "${_active_key}")"
  _generation_json="$(__json_quote "${_generation}")"

  {
    if [[ "${_active_exists}" == true ]]; then
      _current_generation_json="$(__json_quote "${_active_generation}")"
      printf 'val(%s) = %s\n' "${_active_key_json}" "${_current_generation_json}"
      printf 'mod(%s) = "%s"\n' "${_active_key_json}" "${_active_mod_revision}"
    else
      printf 'create(%s) = "0"\n' "${_active_key_json}"
    fi
    printf '\n'
    printf 'put %s %s\n' "${_active_key_json}" "${_generation_json}"
    printf '\n'
    printf '\n'
  } >"${_pointer_txn}"

  "${_ETCDCTL}" txn <"${_pointer_txn}" >"${_pointer_output}"
  if [[ "$(sed -n '1p' "${_pointer_output}")" != 'SUCCESS' ]]; then
    printf '%s\n' 'active pointer changed during publication; retry pack publication' >&2
    return 1
  fi
}

__main() {
  while [[ "${#}" -gt 0 ]]; do
    case "${1}" in
      --pack-dir)
        if [[ "${#}" -lt 2 ]]; then
          __usage
          return 2
        fi
        _PACK_DIR="${2}"
        shift 2
        ;;
      -h|--help)
        __usage
        return 0
        ;;
      *)
        __usage
        return 2
        ;;
    esac
  done

  if [[ -z "${_PACK_DIR}" ]]; then
    __usage
    return 2
  fi

  command -v "${_ETCDCTL}" >/dev/null
  command -v jq >/dev/null
  command -v base64 >/dev/null
  command -v cmp >/dev/null
  command -v sha256sum >/dev/null

  _WORK_DIR="$(mktemp -d)"
  trap '__cleanup' EXIT HUP INT TERM

  __read_metadata
  __stage_bundle
  __capture_active
  if [[ "${_active_generation}" == "${_generation}" ]]; then
    printf '%s\n' "pemcast v5 publication no-op: ${_target_id} ${_generation}"
    return 0
  fi
  __commit_pointer
  printf '%s\n' "pemcast v5 published: ${_target_id} ${_generation}"
}

__cleanup() {
  if [[ -n "${_WORK_DIR:-}" && -d "${_WORK_DIR}" ]]; then
    rm -rf "${_WORK_DIR}"
  fi
}

__main "$@"
