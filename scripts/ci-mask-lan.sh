#!/usr/bin/env bash
# scripts/ci-mask-lan.sh: mask the LAN addresses a pool runner's environment
# names (Tenant Agent ruling 887: internal addresses stay out of public
# repositories, and their CI logs count). The pool sets the DEVCACHE_*
# package caches, GOPROXY's LAN module proxy and the LAN CI registry; on a
# forge, the release registry (RELEASE_REGISTRY) and the forge's own host
# (GITHUB_SERVER_URL), which the image publishing and resolving name. A
# BuildKit build echoes its build arguments and `go env` prints GOPROXY.
# Run before any step that builds: for each entry of those variables whose
# host is internal, it prints ::add-mask:: for the entry, its host:port and
# its host, so the runner masks the address in every later line. A host is
# internal when it is a private IPv4 address, or (ci-template v1.53, Tenant
# Agent on trusted-firmware #821) a host name: a single label with no dot
# (a LAN host name) or a name under .internal, .lan, .local, .localdomain,
# .home.arpa or .corp. Public hosts and keywords (proxy.golang.org, direct,
# off) are left.
# v1.53 (Tenant Agent on go-quicbaas #2667): every variable whose name ends
# in _REGISTRY is scanned (CI_REGISTRY, CI_IMAGE_REGISTRY, RELEASE_REGISTRY,
# a copy's IT_REGISTRY), and the template's mask step sets
# VARS_CI_IMAGE_REGISTRY from the repository variable, because a registry a
# later step takes from `vars` is not in this step's environment otherwise. A
# _REGISTRY_OWNER or other non-host name is not scanned.
# Userinfo (Codex on go-qrdb #1671): an entry's authority is split at its
# last "@" before the host is classified, so in
# http://build.user:password@192.168.1.10:8080 the host is 192.168.1.10, not
# the user name; a bracketed IPv6 host keeps its brackets. The userinfo and
# its password are masked whenever present, on a public host too, and an
# internal host also masks the authority with its userinfo. An "@" in the
# path is no userinfo.
# Host-name case and a terminal dot (Codex on go-xkms #2340): DNS names are
# case-insensitive and may be written fully qualified, so cache.INTERNAL and
# registry.lan. are internal. The host is classified from a copy, lowercased
# with one terminal dot removed; the masks keep the entry's own spelling,
# which is what a log line repeats.
#
#   usage: scripts/ci-mask-lan.sh
set -euo pipefail

# mask <value>: one ::add-mask:: command, its value escaped as workflow
# command data (% as %25, CR as %0D, LF as %0A), because the runner decodes
# those sequences: a literal %25 in a password would otherwise register a
# different mask than the text a log repeats (local Codex review on #1680).
mask() {
	local v=${1//%/%25}
	v=${v//$'\r'/%0D}
	v=${v//$'\n'/%0A}
	printf '::add-mask::%s\n' "$v"
}

# maskhost <value>: a value that names the host, masked as written and,
# when that differs, lowercased, because a helper may log the normalized
# form (Codex round 2 on go-xkms #2346: its ci-registry.sh logs the
# lowercased RELEASE_REGISTRY). Credentials are masked as written only.
maskhost() {
	mask "$1"
	[ "${1,,}" = "$1" ] || mask "${1,,}"
}

private() {
	local h=${1,,}
	h=${h%.}
	case $h in
	10.* | 192.168.* | 172.1[6-9].* | 172.2[0-9].* | 172.3[01].*) return 0 ;;
	'' | direct | off) return 1 ;;
	*.internal | *.lan | *.local | *.localdomain | *.home.arpa | *.corp) return 0 ;;
	*.*) return 1 ;;
	esac
	return 0
}

for v in $(compgen -e); do
	case $v in
	DEVCACHE_* | GOPROXY | *_REGISTRY | GITHUB_SERVER_URL) ;;
	*) continue ;;
	esac
	IFS='|, ' read -ra entries <<<"${!v}"
	for entry in "${entries[@]}"; do
		[ -n "$entry" ] || continue
		# authority: [userinfo@]host[:port], before the first "/" of the path.
		authority=${entry#*://}
		authority=${authority%%/*}
		hostport=${authority##*@}
		userinfo=""
		[ "$hostport" = "$authority" ] || userinfo=${authority%@*}
		case $hostport in
		\[*\]*) host=${hostport%%]*}] ;;
		*) host=${hostport%%:*} ;;
		esac
		if [ -n "$userinfo" ]; then
			mask "$userinfo"
			password=${userinfo#*:}
			[ "$password" = "$userinfo" ] || [ -z "$password" ] || mask "$password"
		fi
		private "$host" || continue
		maskhost "$entry"
		[ "$authority" = "$entry" ] || maskhost "$authority"
		[ "$hostport" = "$authority" ] || maskhost "$hostport"
		[ "$host" = "$hostport" ] || maskhost "$host"
		case $host in \[*\]) inner=${host#[}; maskhost "${inner%]}" ;; esac
	done
done
