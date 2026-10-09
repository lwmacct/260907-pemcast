#!/usr/bin/env bash
set -euo pipefail

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

__main() {
    __require_environment

    _root_password="$(__read_password 'etcd root password: ')"
    _agent_password="$(__read_password 'etcd agent password: ')"
    _publish_password="$(__read_password 'etcd publish password: ')"

    _auth_status="$(__etcdctl_as_root auth status)"
    if ! grep -q 'Authentication Status: true' <<<"$_auth_status"; then
        printf 'etcd authentication must be enabled before initializing pemcast RBAC\n' >&2
        return 1
    fi

    __ensure_user agent "$_agent_password"
    __ensure_user publish "$_publish_password"

    __ensure_role agent
    __ensure_role publish

    __etcdctl_as_root role grant-permission agent read /pemcast/ --prefix
    __etcdctl_as_root role grant-permission publish readwrite /pemcast/ --prefix
    __etcdctl_as_root user grant-role agent agent
    __etcdctl_as_root user grant-role publish publish

    ETCDCTL_USER="agent:$_agent_password" etcdctl get /pemcast/ --prefix >/dev/null
    if ETCDCTL_USER="agent:$_agent_password" etcdctl put /pemcast/rbac-probe denied >/dev/null 2>&1; then
        printf 'agent unexpectedly has etcd write permission\n' >&2
        return 1
    fi

    ETCDCTL_USER="publish:$_publish_password" etcdctl get /pemcast/ --prefix >/dev/null
    if env -u ETCDCTL_USER etcdctl get /pemcast/ --prefix >/dev/null 2>&1; then
        printf 'unauthenticated etcd read unexpectedly succeeded\n' >&2
        return 1
    fi

    printf 'pemcast etcd RBAC initialized\n'
}

__main
