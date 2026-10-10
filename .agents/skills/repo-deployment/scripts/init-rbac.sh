#!/usr/bin/env bash
set -euo pipefail

__usage() {
    printf 'usage: %s [options] <target-id> [<target-id>...]\n' "$0"
    printf 'options:\n'
    printf '  --etcd-prefix <prefix>   default: /pemcast\n'
    printf '  --agent-user <user>      default: agent\n'
    printf '  --publisher-user <user>  default: publish\n'
    printf '  --broad                  grant agent/publish roles the whole namespace prefix\n'
    printf 'PEMCAST_ETCD_PREFIX, PEMCAST_AGENT_USER, PEMCAST_PUBLISHER_USER, PEMCAST_TARGETS and PEMCAST_RBAC_BROAD may also be used.\n'
}

__require_environment() {
    if [[ -z "${ETCDCTL_ENDPOINTS:-}" ]]; then
        printf 'ETCDCTL_ENDPOINTS is required\n' >&2
        return 1
    fi
    export ETCDCTL_ENDPOINTS

    if ! command -v etcdctl >/dev/null 2>&1; then
        printf 'required command not found: etcdctl\n' >&2
        return 1
    fi
}

__read_password() {
    _prompt="$1"
    _password=""

    if [[ -t 0 ]]; then
        read -r -s -p "$_prompt" _password
    else
        read -r -p "$_prompt" _password
    fi
    printf '\n' >&2

    if [[ -z "$_password" ]]; then
        printf 'password must not be empty\n' >&2
        return 1
    fi
    printf '%s' "$_password"
}

__etcdctl_as_root() {
    ETCDCTL_USER="root:$_root_password" etcdctl "$@"
}

__ensure_user() {
    _user_name="$1"
    _user_password="$2"

    if ! __etcdctl_as_root user get "$_user_name" >/dev/null 2>&1; then
        __etcdctl_as_root user add "$_user_name" --new-user-password="$_user_password"
    fi
}

__ensure_role() {
    _role_name="$1"

    if ! __etcdctl_as_root role get "$_role_name" >/dev/null 2>&1; then
        __etcdctl_as_root role add "$_role_name"
    fi
}

__ensure_permission() {
    _role_name="$1"
    _permission="$2"
    _key="$3"
    _prefix="${4:-}"

    _args=(role grant-permission "$_role_name" "$_permission" "$_key")
    if [[ -n "$_prefix" ]]; then
        _args+=(--prefix)
    fi

    if ! __etcdctl_as_root "${_args[@]}" >/dev/null 2>&1; then
        __etcdctl_as_root role get "$_role_name" | grep -F "$_key" >/dev/null
    fi
}

__ensure_user_role() {
    _user_name="$1"
    _role_name="$2"

    if ! __etcdctl_as_root user grant-role "$_user_name" "$_role_name" >/dev/null 2>&1; then
        __etcdctl_as_root user get "$_user_name" | grep -F "$_role_name" >/dev/null
    fi
}

__valid_target() {
    _target="$1"

    [[ "$_target" != "." && "$_target" != ".." ]] || return 1
    [[ "$_target" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]
}

__valid_user_name() {
    [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]
}

__normalize_prefix() {
    _input="$1"
    _prefix="${_input%/}"
    if [[ -z "$_prefix" ]]; then
        _prefix="/"
    fi
    if [[ "$_prefix" != /* || "$_prefix" == *'//' ]]; then
        printf 'unsafe etcd prefix %q\n' "$1" >&2
        return 1
    fi
    if [[ "$_prefix" != "/" ]]; then
        IFS='/' read -r -a _components <<<"$_prefix"
        for _component in "${_components[@]:1}"; do
            if [[ "$_component" == "." || "$_component" == ".." ]]; then
                printf 'unsafe etcd prefix %q\n' "$_input" >&2
                return 1
            fi
        done
    fi
    printf '%s' "$_prefix"
}

__main() {
    __require_environment

    _targets=()
    _raw_targets=()
    _etcd_prefix="${PEMCAST_ETCD_PREFIX:-/pemcast}"
    _agent_user="${PEMCAST_AGENT_USER:-agent}"
    _publisher_user="${PEMCAST_PUBLISHER_USER:-publish}"
    _broad="${PEMCAST_RBAC_BROAD:-false}"

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --etcd-prefix=*)
                _etcd_prefix="${1#*=}"
                shift
                ;;
            --etcd-prefix)
                if [[ $# -lt 2 ]]; then
                    __usage >&2
                    return 1
                fi
                _etcd_prefix="$2"
                shift 2
                ;;
            --agent-user=*)
                _agent_user="${1#*=}"
                shift
                ;;
            --agent-user)
                if [[ $# -lt 2 ]]; then
                    __usage >&2
                    return 1
                fi
                _agent_user="$2"
                shift 2
                ;;
            --publisher-user=*)
                _publisher_user="${1#*=}"
                shift
                ;;
            --publisher-user)
                if [[ $# -lt 2 ]]; then
                    __usage >&2
                    return 1
                fi
                _publisher_user="$2"
                shift 2
                ;;
            --broad)
                _broad=true
                shift
                ;;
            --*)
                printf 'unknown option %q\n' "$1" >&2
                __usage >&2
                return 1
                ;;
            *)
                _raw_targets+=("$1")
                shift
                ;;
        esac
    done

    if [[ ${#_raw_targets[@]} -eq 0 && -n "${PEMCAST_TARGETS:-}" ]]; then
        while IFS= read -r _target; do
            _raw_targets+=("$_target")
        done < <(printf '%s\n' "$PEMCAST_TARGETS" | tr ',' '\n')
    fi

    _protocol_root="$(__normalize_prefix "$_etcd_prefix")/v6"
    if [[ "$_protocol_root" == "//v6" ]]; then
        _protocol_root="/v6"
    fi

    if [[ ${#_raw_targets[@]} -eq 0 ]]; then
        __usage >&2
        return 1
    fi

    for _target in "${_raw_targets[@]}"; do
        if [[ "$_target" == *','* ]]; then
            while IFS= read -r _comma_target; do
                _targets+=("$_comma_target")
            done < <(printf '%s\n' "$_target" | tr ',' '\n')
        else
            _targets+=("$_target")
        fi
    done

    if [[ "${#_targets[@]}" -eq 0 ]]; then
        __usage >&2
        return 1
    fi
    if [[ "${_broad}" != true && "${_broad}" != false ]]; then
        printf 'PEMCAST_RBAC_BROAD must be true or false\n' >&2
        return 1
    fi
    for _target in "${_targets[@]}"; do
        if ! __valid_target "$_target"; then
            printf 'unsafe target id %q\n' "$_target" >&2
            return 1
        fi
    done

    if ! __valid_user_name "$_agent_user"; then
        printf 'unsafe agent user name %q\n' "$_agent_user" >&2
        return 1
    fi
    if ! __valid_user_name "$_publisher_user"; then
        printf 'unsafe publisher user name %q\n' "$_publisher_user" >&2
        return 1
    fi
    if [[ "$_agent_user" == "root" || "$_publisher_user" == "root" ]]; then
        printf 'pemcast users must not use the etcd root user\n' >&2
        return 1
    fi
    if [[ "$_agent_user" == "$_publisher_user" ]]; then
        printf 'agent and publisher users must be different\n' >&2
        return 1
    fi

    _root_password="$(__read_password 'etcd root password: ')"
    _agent_password="$(__read_password "etcd ${_agent_user} password: ")"
    _publisher_password="$(__read_password "etcd ${_publisher_user} password: ")"

    _auth_status="$(__etcdctl_as_root auth status)"
    if ! grep -q 'Authentication Status: true' <<<"$_auth_status"; then
        printf 'etcd authentication must be enabled before initializing pemcast RBAC\n' >&2
        return 1
    fi

    __ensure_user "$_agent_user" "$_agent_password"
    __ensure_user "$_publisher_user" "$_publisher_password"

    _active_prefix="${_protocol_root}/active/"
    _bundles_prefix="${_protocol_root}/bundles/"
    if [[ "${_broad}" == true ]]; then
        _namespace_prefix="$(__normalize_prefix "$_etcd_prefix")/"
        if [[ "${_namespace_prefix}" == '//' ]]; then
            _namespace_prefix='/'
        fi

        __ensure_role agent
        __ensure_permission agent read "${_namespace_prefix}" prefix
        __ensure_user_role "$_agent_user" agent

        __ensure_role publish
        __ensure_permission publish readwrite "${_namespace_prefix}" prefix
        __ensure_user_role "$_publisher_user" publish
    else
        _agent_active_role="agent-active:${_protocol_root}"
        __ensure_role "$_agent_active_role"
        __ensure_permission "$_agent_active_role" read "$_active_prefix" prefix
        __ensure_user_role "$_agent_user" "$_agent_active_role"

        for _target in "${_targets[@]}"; do
            _active_key="${_active_prefix}${_target}"
            _target_bundles_prefix="${_bundles_prefix}${_target}/"
            _agent_bundle_role="agent-bundles:${_protocol_root}:${_target}"
            _publisher_role="publisher:${_protocol_root}:${_target}"

            __ensure_role "$_agent_bundle_role"
            __ensure_permission "$_agent_bundle_role" read "$_target_bundles_prefix" prefix
            __ensure_user_role "$_agent_user" "$_agent_bundle_role"

            __ensure_role "$_publisher_role"
            __ensure_permission "$_publisher_role" readwrite "$_active_key"
            __ensure_permission "$_publisher_role" readwrite "$_target_bundles_prefix" prefix
            __ensure_user_role "$_publisher_user" "$_publisher_role"
        done
    fi

    for _target in "${_targets[@]}"; do
        ETCDCTL_USER="${_agent_user}:$_agent_password" etcdctl get \
            "${_active_prefix}${_target}" >/dev/null
    done

    _probe_target="${_targets[0]}"
    _probe_key="${_bundles_prefix}${_probe_target}/rbac-probe-$$"
    ETCDCTL_USER="${_publisher_user}:$_publisher_password" etcdctl put "$_probe_key" probe >/dev/null
    ETCDCTL_USER="${_agent_user}:$_agent_password" etcdctl get "$_probe_key" >/dev/null
    ETCDCTL_USER="${_publisher_user}:$_publisher_password" etcdctl del "$_probe_key" >/dev/null

    if [[ "${_broad}" == false ]]; then
        _denied_target="rbac-denied-$$"
        if ETCDCTL_USER="${_agent_user}:$_agent_password" etcdctl get \
            "${_bundles_prefix}${_denied_target}/probe" >/dev/null 2>&1; then
            printf 'agent unexpectedly has access to an unauthorized target bundle\n' >&2
            return 1
        fi
    fi
    if ETCDCTL_USER="${_agent_user}:$_agent_password" etcdctl put "$_probe_key" denied >/dev/null 2>&1; then
        printf 'agent unexpectedly has bundle write permission\n' >&2
        ETCDCTL_USER="${_publisher_user}:$_publisher_password" etcdctl del "$_probe_key" >/dev/null || true
        return 1
    fi
    if env -u ETCDCTL_USER etcdctl get "$_probe_key" >/dev/null 2>&1; then
        printf 'unauthenticated etcd read unexpectedly succeeded\n' >&2
        return 1
    fi

    if [[ "${_broad}" == true ]]; then
        printf 'pemcast v6 broad RBAC initialized below %s for users %s/%s: %s\n' \
            "$_namespace_prefix" "$_agent_user" "$_publisher_user" "${_targets[*]}"
    else
        printf 'pemcast v6 target RBAC initialized below %s for users %s/%s: %s\n' \
            "$_protocol_root" "$_agent_user" "$_publisher_user" "${_targets[*]}"
    fi
}

__main "$@"
