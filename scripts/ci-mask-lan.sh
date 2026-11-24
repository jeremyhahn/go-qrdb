#!/usr/bin/env bash
# ci-mask-lan.sh: mask the LAN addresses a runner's environment names.
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
