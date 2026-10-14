#!/bin/sh
#
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
#
# cluster-cpus.sh — the cluster stage's CPU cap, bounded by the docker host.
#
#   cluster-cpus.sh CPUS   print the smaller of CPUS and the docker host's CPUs
#
# The cluster stage caps its integration-tests container at CLUSTER_CPUS
# (`cpus:`, docker --cpus; #682). dockerd refuses a cap above the CPUs it has
# ("range of CPUs is from 0.01 to 4.00"), so a fixed cap of 8 never starts the
# container on a 4-CPU runner and the job runs no test at all (#673). The cap
# stays a bound, not a demand: on a host with fewer CPUs it is the host's
# count, which is the budget docker would give an uncapped container there.
#
# The count is the docker DAEMON's (`docker info` NCPU), not this shell's
# nproc: a CI job runs in a container of its own and starts the stack through
# the host's dockerd, and NCPU is the figure dockerd checks --cpus against.
#
# The decision goes to stderr, so the job log says which budget a run had; the
# cap alone goes to stdout. CPUS=0 is docker's "unset" and passes through
# without asking the daemon.
#
# Environment:
#   DOCKER   docker command (default: docker)

set -eu

DOCKER=${DOCKER:-docker}

die() {
    printf 'cluster-cpus: %s\n' "$1" >&2
    exit 1
}

[ $# -eq 1 ] || die "usage: $0 CPUS"
want=$1

case $want in
    0)
        printf 'cluster-cpus: CLUSTER_CPUS=0, no CPU cap\n' >&2
        printf '0\n'
        exit 0
        ;;
    '' | *[!0-9]* | 0*) die "CLUSTER_CPUS must be a non-negative integer, got '$want'" ;;
esac

host=$("$DOCKER" info --format '{{.NCPU}}') || die "docker info failed: cannot read the docker host's CPU count"
case $host in
    '' | *[!0-9]* | 0*) die "docker info reported '$host' CPUs, not a positive integer" ;;
esac

cap=$want
if [ "$host" -lt "$want" ]; then
    cap=$host
fi
printf 'cluster-cpus: capping the cluster stage at %s CPUs (CLUSTER_CPUS=%s, docker host has %s)\n' "$cap" "$want" "$host" >&2
printf '%s\n' "$cap"
