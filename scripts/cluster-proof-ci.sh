#!/usr/bin/env bash
# Copyright 2026 Jeremy Hahn
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# cluster-proof-ci.sh: the cluster proof on one machine, as the nightly
# `clusterproof-soak` job runs it (docs/cluster-verification/user.md).
#
#   render DIR IMAGE  write a throwaway test CA, node and admin certificates,
#                     PINs and the inventory, targets and workload files for a
#                     three-node cluster of IMAGE on the docker network
#                     "proof" (192.0.2.0/24, RFC 5737). The last line on
#                     stdout is the PROOF_NODES value.
#   up DIR            create the network and boot the cluster with BOOT-01,
#                     leaving it running (PROOF_KEEP=1).
#   down DIR          remove the containers and the network.
#
# Every fault runs locally through docker: no ssh, no unseal (PROOF_SEAL=none).
set -euo pipefail

readonly network=proof
readonly subnet=192.0.2.0/24
readonly nodes=(1 2 3)
readonly quic_port=63004

usage() {
	echo "usage: $0 render DIR IMAGE | up DIR | down DIR" >&2
	exit 2
}

# address N prints node N's IP on the proof network.
address() { printf '192.0.2.1%s' "$1"; }

# absdir DIR creates DIR and prints its absolute path: docker bind mounts
# and the harness both need one.
absdir() {
	mkdir -p "$1"
	(cd "$1" && pwd)
}

# genkey FILE writes a P-256 private key.
genkey() {
	openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$1" 2>/dev/null
}

# sign CA_DIR KEY CERT SUBJECT EXTENSIONS issues CERT for KEY from the CA.
sign() {
	local ca=$1 key=$2 cert=$3 subject=$4 ext=$5 csr extfile
	csr=$(mktemp)
	extfile=$(mktemp)
	printf '%s\n' "$ext" >"$extfile"
	openssl req -new -key "$key" -subj "$subject" -out "$csr"
	openssl x509 -req -in "$csr" -CA "$ca/ca.pem" -CAkey "$ca/ca-key.pem" \
		-CAcreateserial -CAserial "$ca/ca.srl" -days 2 -sha256 \
		-extfile "$extfile" -out "$cert" 2>/dev/null
	rm -f "$csr" "$extfile"
}

# pin FILE writes a random PIN with no trailing newline.
pin() { printf '%s' "$(openssl rand -hex 8)" >"$1"; }

render() {
	local dir image n addr tls
	dir=$(absdir "$1")
	image=$2
	command -v openssl >/dev/null || {
		echo "$0: openssl is required" >&2
		exit 1
	}

	mkdir -p "$dir/tls"
	genkey "$dir/tls/ca-key.pem"
	openssl req -x509 -new -key "$dir/tls/ca-key.pem" -subj "/CN=clusterproof-ci-ca" \
		-days 2 -sha256 -out "$dir/tls/ca.pem" \
		-addext "basicConstraints=critical,CA:TRUE" \
		-addext "keyUsage=critical,keyCertSign,cRLSign"

	for n in "${nodes[@]}"; do
		addr=$(address "$n")
		tls=$dir/node-$n/tls
		mkdir -p "$tls" "$dir/node-$n/data"
		# The node runs as the image's user, not the runner's: the data
		# directory must be writable and the key readable by it.
		chmod 0777 "$dir/node-$n/data"
		cp "$dir/tls/ca.pem" "$tls/ca.pem"
		genkey "$tls/node-key.pem"
		sign "$dir/tls" "$tls/node-key.pem" "$tls/node.pem" "/CN=node-$n" \
			"$(printf '%s\n' \
				"subjectAltName=IP:$addr" \
				"keyUsage=critical,digitalSignature" \
				"extendedKeyUsage=serverAuth,clientAuth")"
		chmod 0644 "$tls/node-key.pem"
	done

	genkey "$dir/admin-key.pem"
	sign "$dir/tls" "$dir/admin-key.pem" "$dir/admin.pem" "/CN=clusterproof-admin" \
		"$(printf '%s\n' \
			"keyUsage=critical,digitalSignature" \
			"extendedKeyUsage=clientAuth")"
	pin "$dir/so.pin"
	pin "$dir/user.pin"

	write_inventory "$dir" "$image"
	write_targets "$dir" "$image"
	write_workload "$dir"

	local list=()
	for n in "${nodes[@]}"; do
		list+=("node-$n=$(address "$n"):$quic_port")
	done
	(
		IFS=,
		printf '%s\n' "${list[*]}"
	)
}

write_inventory() {
	local dir=$1 image=$2 n sep=""
	{
		printf '{\n'
		printf '  "image": "%s",\n' "$image"
		printf '  "network": "%s",\n' "$network"
		printf '  "credentials": {\n'
		printf '    "ca_file": "%s/tls/ca.pem",\n' "$dir"
		printf '    "cert_file": "%s/admin.pem",\n' "$dir"
		printf '    "key_file": "%s/admin-key.pem"\n' "$dir"
		printf '  },\n'
		printf '  "first_boot": {\n'
		printf '    "so_pin_file": "%s/so.pin",\n' "$dir"
		printf '    "user_pin_file": "%s/user.pin"\n' "$dir"
		printf '  },\n'
		printf '  "nodes": [\n'
		for n in "${nodes[@]}"; do
			printf '%s    {"name": "node-%s", "replica_id": %s, "address": "%s",\n' "$sep" "$n" "$n" "$(address "$n")"
			printf '     "tls_dir": "%s/node-%s/tls", "data": "%s/node-%s/data"}' "$dir" "$n" "$dir" "$n"
			sep=$',\n'
		done
		printf '\n  ]\n}\n'
	} >"$dir/inventory.json"
}

write_targets() {
	local dir=$1 image=$2 n sep=""
	{
		printf '{\n'
		printf '  "exec": ["docker", "run", "--rm", "--privileged",\n'
		printf '           "--net=container:{{.Vars.container}}",\n'
		printf '           "-v", "{{.Vars.data_dir}}:/data", "-v", "/sys/fs/cgroup:/sys/fs/cgroup",\n'
		printf '           "{{.Vars.fault_image}}", "{{shquote .Argv}}"],\n'
		printf '  "signal": ["docker", "kill", "--signal={{.Signal}}", "{{.Vars.container}}"],\n'
		printf '  "restart": ["docker", "start", "{{.Vars.container}}"],\n'
		printf '  "nodes": {\n'
		for n in "${nodes[@]}"; do
			printf '%s    "node-%s": {\n' "$sep" "$n"
			printf '      "vars": {"container": "clusterproof-node-%s", "data_dir": "%s/node-%s/data",\n' "$n" "$dir" "$n"
			printf '               "fault_image": "%s"},\n' "$image"
			printf '      "addresses": ["%s"]\n' "$(address "$n")"
			printf '    }'
			sep=$',\n'
		done
		printf '\n  },\n'
		printf '  "disk_full": {"path": "/data"},\n'
		printf '  "schedule": {"min_gap": "30s", "max_gap": "2m", "min_length": "10s", "max_length": "1m"},\n'
		printf '  "heal_timeout": "1m",\n'
		printf '  "recovery_timeout": "2m",\n'
		printf '  "declared": {"read_mode": "raft.lease_read=true (lease reads on)"}\n'
		printf '}\n'
	} >"$dir/targets.json"
}

write_workload() {
	local dir=$1 n eps=()
	for n in "${nodes[@]}"; do
		eps+=("\"$(address "$n"):$quic_port\"")
	done
	{
		printf '{\n'
		printf '  "endpoints": [%s, %s, %s],\n' "${eps[@]}"
		printf '  "protocol": "quic",\n'
		printf '  "credentials": {\n'
		printf '    "ca_file": "%s/tls/ca.pem",\n' "$dir"
		printf '    "cert_file": "%s/admin.pem",\n' "$dir"
		printf '    "key_file": "%s/admin-key.pem"\n' "$dir"
		printf '  },\n'
		printf '  "clients": 12,\n'
		printf '  "keys": 64,\n'
		printf '  "mix": {"put": 40, "get": 40, "delete": 5, "cas": 15},\n'
		printf '  "op_timeout": "5s",\n'
		printf '  "redial_backoff": "100ms",\n'
		printf '  "max_backoff": "2s"\n'
		printf '}\n'
	} >"$dir/workload.json"
}

up() {
	local dir
	dir=$(absdir "$1")
	if ! docker network inspect "$network" >/dev/null 2>&1; then
		docker network create --subnet "$subnet" "$network" >/dev/null
	fi
	PROOF_INVENTORY=$dir/inventory.json PROOF_KEEP=1 make cluster-proof-boot
}

down() {
	local n
	for n in "${nodes[@]}"; do
		docker rm -f "clusterproof-node-$n" >/dev/null 2>&1 || true
	done
	docker network rm "$network" >/dev/null 2>&1 || true
}

case ${1:-} in
render)
	[[ $# -eq 3 ]] || usage
	render "$2" "$3"
	;;
up)
	[[ $# -eq 2 ]] || usage
	up "$2"
	;;
down)
	[[ $# -eq 2 ]] || usage
	down
	;;
*) usage ;;
esac
