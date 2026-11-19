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
#   render DIR IMAGE [FAULT_IMAGE]
#                     write a throwaway test CA, node and admin certificates,
#                     PINs and the inventory, targets and workload files for a
#                     three-node cluster of IMAGE on the docker network
#                     "proof" (192.0.2.0/24, RFC 5737). Faults run in
#                     FAULT_IMAGE (default IMAGE), which must carry the tools
#                     the fault commands run: `make cluster-proof-fault-image`
#                     builds one (test/clusterproof/fault.Dockerfile). The
#                     last line on stdout is the PROOF_NODES value. Node
#                     certificates carry spiffe://qrdb.cluster/node/<ip>:63001,
#                     the admin certificate OU=system, CN=admin and
#                     spiffe://system/user/admin, as the ceremony adopts it.
#   up DIR            give each node its own CLUSTERPROOF_DISK_MB (default
#                     1024) ext4 filesystem, loop-mounted on DIR/node-N/data,
#                     create the network and boot the cluster with BOOT-01,
#                     leaving it running (PROOF_KEEP=1).
#   down DIR          remove the containers and the network, then unmount
#                     and delete the node filesystems.
#   logs DIR          write every node's full `docker logs` to
#                     DIR/logs/node-N.log (beside DIR/bundle, outside the
#                     checksummed bundle); up does the same itself
#                     when it exits non-zero (BOOT-01 failing included).
#
# Every fault runs locally through docker: no ssh, no unseal (PROOF_SEAL=none).
# The fault container shares the node's network namespace and binds the
# node's own filesystem at /data, so disk-full fills only that node; the
# fault's argv reaches it word by word ("$@", no shell in between). up and
# down use sudo for the loop mounts unless run as root; render writes files
# only.
set -euo pipefail

readonly network=proof
readonly subnet=192.0.2.0/24
readonly nodes=(1 2 3)
readonly quic_port=63004
# Each node container's cap. The soak runs beside its own runner (a hosted
# runner has 4 CPUs and 16 GB): three nodes at 3 GB and 1 CPU each leave
# the runner, the soak process and the fault containers a CPU and 7 GB, so
# the cluster cannot starve the runner of the CPU or memory it needs to
# keep talking to the server.
readonly node_memory=3g
readonly node_cpus=1
# The node certificates name each node by its Raft address in the cluster
# trust domain (the config default; BOOT-01 sets none), as cluster
# enrolment does: test/clusterproof/container/inventory.go RaftPort.
readonly trust_domain=qrdb.cluster
readonly raft_port=63001

usage() {
	echo "usage: $0 render DIR IMAGE [FAULT_IMAGE] | up DIR | down DIR | logs DIR" >&2
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
	local dir image fault_image n addr tls
	dir=$(absdir "$1")
	image=$2
	fault_image=${3:-$image}
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
				"subjectAltName=IP:$addr,URI:spiffe://$trust_domain/node/$addr:$raft_port" \
				"keyUsage=critical,digitalSignature" \
				"extendedKeyUsage=serverAuth,clientAuth")"
		chmod 0644 "$tls/node-key.pem"
	done

	genkey "$dir/admin-key.pem"
	# The ceremony adopts this as the cluster admin: CN admin, Subject OU[0]
	# the system tenant, and the SPIFFE user SAN the ceremony itself issues.
	sign "$dir/tls" "$dir/admin-key.pem" "$dir/admin.pem" "/OU=system/CN=admin" \
		"$(printf '%s\n' \
			"subjectAltName=URI:spiffe://system/user/admin" \
			"keyUsage=critical,digitalSignature" \
			"extendedKeyUsage=clientAuth")"
	pin "$dir/so.pin"
	pin "$dir/user.pin"

	write_inventory "$dir" "$image"
	write_targets "$dir" "$fault_image"
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
		printf '  "memory": "%s",\n' "$node_memory"
		printf '  "cpus": "%s",\n' "$node_cpus"
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
	local dir=$1 fault_image=$2 n sep=""
	{
		printf '{\n'
		printf '  "exec": ["docker", "run", "--rm", "--privileged",\n'
		printf '           "--net=container:{{.Vars.container}}",\n'
		printf '           "-v", "{{.Vars.data_dir}}:/data", "-v", "/sys/fs/cgroup:/sys/fs/cgroup",\n'
		# "$@" (nemesis.ArgvElement) expands to the fault's argv word by
		# word: the wrapper runs without a shell, so one quoted string
		# would reach docker as a single executable name.
		printf '           "{{.Vars.fault_image}}", "$@"],\n'
		# disk-full fills and frees through disk_exec: the data volume only, no
		# --net=container, so the heal still runs when the node exited on ENOSPC.
		printf '  "disk_exec": ["docker", "run", "--rm", "--privileged",\n'
		printf '                "-v", "{{.Vars.data_dir}}:/data", "{{.Vars.fault_image}}", "$@"],\n'
		printf '  "signal": ["docker", "kill", "--signal={{.Signal}}", "{{.Vars.container}}"],\n'
		printf '  "restart": ["docker", "start", "{{.Vars.container}}"],\n'
		printf '  "nodes": {\n'
		for n in "${nodes[@]}"; do
			printf '%s    "node-%s": {\n' "$sep" "$n"
			printf '      "vars": {"container": "clusterproof-node-%s", "data_dir": "%s/node-%s/data",\n' "$n" "$dir" "$n"
			printf '               "fault_image": "%s"},\n' "$fault_image"
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
	# A failing up (BOOT-01 included) keeps every node's full log in
	# DIR/logs, which the CI job uploads beside the bundle.
	logs_dir=$dir
	trap on_exit EXIT
	mount_data "$dir"
	if ! docker network inspect "$network" >/dev/null 2>&1; then
		docker network create --subnet "$subnet" "$network" >/dev/null
	fi
	PROOF_INVENTORY=$dir/inventory.json PROOF_KEEP=1 make cluster-proof-boot
}

# mount_data DIR gives each node its own filesystem: a CLUSTERPROOF_DISK_MB
# (default 1024) ext4 image, loop-mounted on DIR/node-N/data. The node and
# its fault container both bind that mountpoint at /data, so disk-full fills
# only that node's filesystem, not the runner's disk.
mount_data() {
	local dir=$1 n data img mb=${CLUSTERPROOF_DISK_MB:-1024} SUDO=""
	[ "$(id -u)" = 0 ] || SUDO=sudo
	for n in "${nodes[@]}"; do
		data=$dir/node-$n/data
		img=$dir/node-$n/data.img
		mkdir -p "$data"
		if mountpoint -q "$data"; then
			continue
		fi
		rm -f "$img"
		truncate -s "${mb}M" "$img"
		mkfs.ext4 -q -F "$img"
		$SUDO mount -o loop "$img" "$data"
		# The release image declares no USER, so the node runs as root;
		# open the mount to any uid so a later non-root image still
		# writes, and leave the data directory empty for first boot.
		$SUDO rmdir "$data/lost+found"
		$SUDO chmod 0777 "$data"
	done
}

# umount_data DIR releases the loop mounts mount_data made.
umount_data() {
	local dir=$1 n data SUDO=""
	[ "$(id -u)" = 0 ] || SUDO=sudo
	for n in "${nodes[@]}"; do
		data=$dir/node-$n/data
		if mountpoint -q "$data" 2>/dev/null; then
			$SUDO umount "$data" || true
		fi
		rm -f "$dir/node-$n/data.img"
	done
}

# save_logs DIR writes `docker logs` of every node, stdout and stderr, to
# DIR/logs/node-N.log, beside DIR/bundle and never inside it: the bundle's
# manifest checksums every file under bundle/, and a log it does not list
# fails the check. CI uploads DIR/logs as the clusterproof-node-logs
# artifact, so each node's whole log survives, not the tail a test prints.
save_logs() {
	local dir=$1 n
	mkdir -p "$dir/logs"
	for n in "${nodes[@]}"; do
		docker logs "clusterproof-node-$n" >"$dir/logs/node-$n.log" 2>&1 || true
	done
	echo "$0: node logs in $dir/logs" >&2
}

# logs_dir is the cluster directory under whose logs/ (beside bundle/) a
# failing exit writes the node logs; empty, a failing exit writes none.
logs_dir=""

# on_exit is the EXIT trap: on a non-zero status it saves the node logs.
on_exit() {
	local rc=$?
	if [ "$rc" -ne 0 ] && [ -n "$logs_dir" ]; then
		save_logs "$logs_dir"
	fi
	exit "$rc"
}

down() {
	local n dir=$1
	for n in "${nodes[@]}"; do
		docker rm -f "clusterproof-node-$n" >/dev/null 2>&1 || true
	done
	docker network rm "$network" >/dev/null 2>&1 || true
	umount_data "$dir"
}

case ${1:-} in
render)
	[[ $# -eq 3 || $# -eq 4 ]] || usage
	render "$2" "$3" "${4:-}"
	;;
up)
	[[ $# -eq 2 ]] || usage
	up "$2"
	;;
down)
	[[ $# -eq 2 ]] || usage
	down "$2"
	;;
logs)
	[[ $# -eq 2 ]] || usage
	save_logs "$(absdir "$2")"
	;;
*) usage ;;
esac
