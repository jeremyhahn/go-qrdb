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
# cluster-cpus.sh: the cluster stage's CPU cap, bounded by the docker host.

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
