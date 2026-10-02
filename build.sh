#!/usr/bin/env bash
# Compila server, record, inspect e discover para Linux, Windows e macOS,
# em amd64 e ARM.
set -euo pipefail

root=$(cd "$(dirname "$0")" && pwd)
out="${root}/dist"
ldflags="-s -w"

commands=(server record inspect discover)

# os/arch[/goarm]
targets=(
	linux/amd64
	linux/arm64
	linux/arm/7
	windows/amd64
	windows/arm64
	darwin/amd64
	darwin/arm64
)

rm -rf "${out}"
mkdir -p "${out}"

for spec in "${targets[@]}"; do
	IFS=/ read -r goos goarch goarm <<<"${spec}"
	dir="${goos}-${goarch}"
	if [[ -n "${goarm}" ]]; then
		dir="${dir}v${goarm}"
	fi
	dest="${out}/${dir}"
	mkdir -p "${dest}"

	echo "==> ${dir}"
	for cmd in "${commands[@]}"; do
		name="${cmd}"
		if [[ "${goos}" == windows ]]; then
			name="${cmd}.exe"
		fi
		env CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" GOARM="${goarm:-}" \
			go build -trimpath -ldflags "${ldflags}" \
			-o "${dest}/${name}" "${root}/cmd/${cmd}"
	done
done

echo
echo "pronto em ${out}/"
find "${out}" -type f | sort
