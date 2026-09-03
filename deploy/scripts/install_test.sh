#!/bin/sh
#
# install_test.sh exercises deploy/scripts/install.sh against the fixed host
# paths only inside disposable containers. It must never run on a host.

set -u

SCRIPT_DIR=$(dirname "$0")
SCRIPT_DIR=$(cd "$SCRIPT_DIR" && pwd)
INSTALLER="$SCRIPT_DIR/install.sh"
TEST_WORK=""
MOCK_BIN=""
MOCK_LOG=""
STDOUT_FILE=""
STDERR_FILE=""
TEST_RC=0
PASS_COUNT=0
FAIL_COUNT=0
OS_RELEASE_BACKUP=""
DSM_VERSION_BACKUP=""
HAD_OS_RELEASE=0
HAD_DSM_VERSION=0

fail() {
  printf 'FAIL %s\n' "$*" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
}

pass() {
  printf 'PASS %s\n' "$1"
  PASS_COUNT=$((PASS_COUNT + 1))
}

require_test_container() {
  if [ "${AZATH_INSTALL_TEST_CONTAINER:-}" != "1" ]; then
    printf 'AZATH_INSTALL_TEST_CONTAINER=1 is required\n' >&2
    exit 2
  fi

  if [ ! -f /.dockerenv ] && [ ! -f /run/.containerenv ]; then
    printf 'installer tests must run in a disposable container\n' >&2
    exit 2
  fi
}

save_platform_files() {
  OS_RELEASE_BACKUP="$TEST_WORK/os-release"
  DSM_VERSION_BACKUP="$TEST_WORK/dsm-version"

  if [ -e /etc/os-release ]; then
    cp -p /etc/os-release "$OS_RELEASE_BACKUP"
    HAD_OS_RELEASE=1
  fi
  if [ -e /etc.defaults/VERSION ]; then
    mkdir -p "$TEST_WORK/etc.defaults"
    cp -p /etc.defaults/VERSION "$DSM_VERSION_BACKUP"
    HAD_DSM_VERSION=1
  fi
}

restore_platform_files() {
  if [ "$HAD_OS_RELEASE" -eq 1 ]; then
    cp -p "$OS_RELEASE_BACKUP" /etc/os-release
  else
    rm -f /etc/os-release
  fi

  if [ "$HAD_DSM_VERSION" -eq 1 ]; then
    mkdir -p /etc.defaults
    cp -p "$DSM_VERSION_BACKUP" /etc.defaults/VERSION
  else
    rm -f /etc.defaults/VERSION
    rmdir /etc.defaults 2>/dev/null || true
  fi
}

reset_target_paths() {
  rm -f /usr/local/bin/azath /usr/local/bin/.azath.* 2>/dev/null || true
  rm -rf /etc/azath
  rm -f /usr/local/lib/systemd/system/azath.service 2>/dev/null || true
  rm -f /etc/systemd/system/azath.service /etc/systemd/system/azath-operator.conf 2>/dev/null || true
  rm -rf /etc/systemd/system/azath.service.d
}

cleanup() {
  restore_platform_files
  reset_target_paths
  if [ -n "$TEST_WORK" ]; then
    rm -rf "$TEST_WORK"
  fi
}

setup_mock_command() {
  command_name=$1
  cat > "$MOCK_BIN/$command_name"
  chmod 700 "$MOCK_BIN/$command_name"
}

setup_mocks() {
  MOCK_BIN="$TEST_WORK/mock-bin"
  MOCK_LOG="$TEST_WORK/mock.log"
  STDOUT_FILE="$TEST_WORK/stdout"
  STDERR_FILE="$TEST_WORK/stderr"
  rm -rf "$MOCK_BIN"
  mkdir -p "$MOCK_BIN"
  : > "$MOCK_LOG"

  setup_mock_command id <<'EOF'
#!/bin/sh
printf 'id' >> "$MOCK_LOG"
for argument in "$@"; do
  printf ' <%s>' "$argument" >> "$MOCK_LOG"
done
printf '\n' >> "$MOCK_LOG"
if [ "$1" = "-u" ] && [ "${2:-}" = "azath" ]; then
  [ "${MOCK_AZATH_EXISTS:-0}" = 1 ] || exit 1
  printf '123\n'
  exit 0
fi
if [ "$1" = "-gn" ] && [ "${2:-}" = "azath" ]; then
  printf '%s\n' "${MOCK_AZATH_GROUP:-azath}"
  exit 0
fi
if [ "$1" = "-u" ]; then
  printf '%s\n' "${MOCK_ID_UID:-0}"
  exit 0
fi
exit 1
EOF

  setup_mock_command uname <<'EOF'
#!/bin/sh
if [ "$1" = "-m" ]; then
  printf '%s\n' "${MOCK_UNAME_MACHINE:-x86_64}"
  exit 0
fi
exit 1
EOF

  setup_mock_command curl <<'EOF'
#!/bin/sh
printf 'curl' >> "$MOCK_LOG"
output_file=""
write_out=0
url=""
while [ "$#" -gt 0 ]; do
  printf ' <%s>' "$1" >> "$MOCK_LOG"
  case "$1" in
    --output|-o)
      shift
      output_file=$1
      ;;
    --write-out|-w)
      shift
      write_out=1
      ;;
    https://*)
      url=$1
      ;;
  esac
  shift
done
printf '\n' >> "$MOCK_LOG"

basename=${url##*/}
status=200
if [ -n "${MOCK_404_BASENAME:-}" ] && [ "$basename" = "$MOCK_404_BASENAME" ]; then
  status=404
elif [ -z "${MOCK_FIXTURE_DIR:-}" ] || [ ! -f "$MOCK_FIXTURE_DIR/$basename" ]; then
  status=404
fi

if [ -n "${MOCK_CURL_TRANSPORT_EXIT:-}" ]; then
  exit "$MOCK_CURL_TRANSPORT_EXIT"
fi
if [ "${MOCK_CURL_BLOCK:-0}" = 1 ]; then
  printf '%s %s\n' "${output_file%/*}" "$$" > "$MOCK_CURL_STARTED_FILE"
  while :; do
    /bin/sleep 1
  done
fi
if [ "$status" = 200 ]; then
  cp "$MOCK_FIXTURE_DIR/$basename" "$output_file"
  [ "$write_out" -eq 0 ] || printf '200'
  exit 0
fi
[ "$write_out" -eq 0 ] || printf '404'
exit 22
EOF

  setup_mock_command chown <<'EOF'
#!/bin/sh
printf 'chown' >> "$MOCK_LOG"
for argument in "$@"; do
  printf ' <%s>' "$argument" >> "$MOCK_LOG"
done
printf '\n' >> "$MOCK_LOG"
EOF

  setup_mock_command getent <<'EOF'
#!/bin/sh
printf 'getent' >> "$MOCK_LOG"
for argument in "$@"; do
  printf ' <%s>' "$argument" >> "$MOCK_LOG"
done
printf '\n' >> "$MOCK_LOG"
[ "${MOCK_GROUP_EXISTS:-0}" = 1 ]
EOF

  setup_mock_command groupadd <<'EOF'
#!/bin/sh
printf 'groupadd\n' >> "$MOCK_LOG"
EOF

  setup_mock_command useradd <<'EOF'
#!/bin/sh
printf 'useradd\n' >> "$MOCK_LOG"
EOF

  setup_mock_command systemctl <<'EOF'
#!/bin/sh
printf 'systemctl' >> "$MOCK_LOG"
for argument in "$@"; do
  printf ' <%s>' "$argument" >> "$MOCK_LOG"
done
printf '\n' >> "$MOCK_LOG"
case "$1" in
  is-active) exit "${MOCK_SERVICE_STATUS:-4}" ;;
  restart) exit "${MOCK_RESTART_STATUS:-0}" ;;
  daemon-reload|status) exit "${MOCK_DAEMON_RELOAD_STATUS:-0}" ;;
esac
exit 1
EOF

  setup_mock_command mktemp <<'EOF'
#!/bin/sh
printf 'mktemp' >> "$MOCK_LOG"
template=""
for argument in "$@"; do
  printf ' <%s>' "$argument" >> "$MOCK_LOG"
  template=$argument
done
printf '\n' >> "$MOCK_LOG"
case "$template" in
  *XXXXXX)
    result="${template%XXXXXX}test.$$"
    : > "$result"
    printf '%s\n' "$result"
    ;;
  *)
    mkdir -p "$MOCK_MKTEMP_DIR"
    printf '%s\n' "$MOCK_MKTEMP_DIR"
    ;;
esac
EOF

  setup_mock_command mv <<'EOF'
#!/bin/sh
printf 'mv' >> "$MOCK_LOG"
for argument in "$@"; do
  printf ' <%s>' "$argument" >> "$MOCK_LOG"
done
printf '\n' >> "$MOCK_LOG"
if [ -n "${MOCK_MV_FAIL_DESTINATION:-}" ] && [ "${2:-}" = "$MOCK_MV_FAIL_DESTINATION" ]; then
  exit 1
fi
exec /bin/mv "$@"
EOF

  for command_name in awk cat chmod cmp cp grep gzip mkdir rm sha256sum tar tr wc; do
    command_path=$(command -v "$command_name") || {
      printf 'test harness requires %s\n' "$command_name" >&2
      exit 2
    }
    ln -s "$command_path" "$MOCK_BIN/$command_name"
  done
}

write_debian_release() {
  cat > /etc/os-release <<'EOF'
ID=debian
VERSION_ID=13
EOF
  rm -f /etc.defaults/VERSION
}

write_dsm_release() {
  cat > /etc/os-release <<'EOF'
ID=other
VERSION_ID=1
EOF
  mkdir -p /etc.defaults
  cat > /etc.defaults/VERSION <<'EOF'
majorversion="7"
EOF
}

write_ambiguous_release() {
  write_debian_release
  mkdir -p /etc.defaults
  cat > /etc.defaults/VERSION <<'EOF'
majorversion="7"
EOF
}

write_unsupported_debian_release() {
  cat > /etc/os-release <<'EOF'
ID=debian
VERSION_ID=12
EOF
  rm -f /etc.defaults/VERSION
}

write_unsupported_dsm_release() {
  cat > /etc/os-release <<'EOF'
ID=other
VERSION_ID=1
EOF
  mkdir -p /etc.defaults
  cat > /etc.defaults/VERSION <<'EOF'
majorversion="6"
EOF
}

write_missing_release() {
  cat > /etc/os-release <<'EOF'
ID=other
VERSION_ID=1
EOF
  rm -f /etc.defaults/VERSION
}

write_candidate() {
  fixture_version=$1
  candidate_version_status=${2:-0}
  cat > "$MOCK_FIXTURE_DIR/azath" <<EOF
#!/bin/sh
if [ "\${1:-}" = "--version" ]; then
  printf '%s (commit: test)\\n' '$fixture_version'
  exit '$candidate_version_status'
fi
exit 1
EOF
  chmod 755 "$MOCK_FIXTURE_DIR/azath"
}

refresh_checksums() {
  (
    cd "$MOCK_FIXTURE_DIR" || exit 1
    sha256sum ./*.tar.gz | awk '{ sub(/^\.\//, "", $2); print }' > checksums.txt
  )
}

rebuild_stable_archives() {
  (
    cd "$MOCK_FIXTURE_DIR" || exit 1
    rm -f azath_linux_amd64.tar.gz azath_linux_arm64.tar.gz
    tar -czf azath_linux_amd64.tar.gz "$@"
    tar -czf azath_linux_arm64.tar.gz "$@"
  )
  refresh_checksums
}

create_release_fixtures() {
  fixture_version=$1
  MOCK_FIXTURE_DIR="$TEST_WORK/fixtures"
  mkdir -p "$MOCK_FIXTURE_DIR/deploy/systemd"
  write_candidate "$fixture_version"
  printf '[Service]\n' > "$MOCK_FIXTURE_DIR/deploy/systemd/azath.service"
  (
    cd "$MOCK_FIXTURE_DIR" || exit 1
    tar -czf azath_linux_amd64.tar.gz azath deploy/systemd/azath.service
    tar -czf azath_linux_arm64.tar.gz azath deploy/systemd/azath.service
    tar -czf azath_0.1.0_linux_amd64.tar.gz azath
  )
  refresh_checksums
}

run_installer() {
  TEST_RC=0
  PATH="$MOCK_BIN" \
    MOCK_LOG="$MOCK_LOG" \
    MOCK_ID_UID="${MOCK_ID_UID:-0}" \
    MOCK_AZATH_EXISTS="${MOCK_AZATH_EXISTS:-0}" \
    MOCK_AZATH_GROUP="${MOCK_AZATH_GROUP:-azath}" \
    MOCK_GROUP_EXISTS="${MOCK_GROUP_EXISTS:-0}" \
    MOCK_SERVICE_STATUS="${MOCK_SERVICE_STATUS:-4}" \
    MOCK_DAEMON_RELOAD_STATUS="${MOCK_DAEMON_RELOAD_STATUS:-0}" \
    MOCK_RESTART_STATUS="${MOCK_RESTART_STATUS:-0}" \
    MOCK_UNAME_MACHINE="${MOCK_UNAME_MACHINE:-x86_64}" \
    MOCK_404_BASENAME="${MOCK_404_BASENAME:-}" \
    MOCK_FIXTURE_DIR="${MOCK_FIXTURE_DIR:-}" \
    MOCK_CURL_TRANSPORT_EXIT="${MOCK_CURL_TRANSPORT_EXIT:-}" \
    MOCK_CURL_BLOCK="${MOCK_CURL_BLOCK:-0}" \
    MOCK_CURL_STARTED_FILE="${MOCK_CURL_STARTED_FILE:-}" \
    MOCK_MV_FAIL_DESTINATION="${MOCK_MV_FAIL_DESTINATION:-}" \
    MOCK_MKTEMP_DIR="$TEST_WORK/installer-tmp" \
    AZATH_VERSION="${AZATH_VERSION:-}" \
    /bin/sh "$INSTALLER" >"$STDOUT_FILE" 2>"$STDERR_FILE" || TEST_RC=$?
}

run_installer_async() {
  TEST_RC=0
  PATH="$MOCK_BIN" \
    MOCK_LOG="$MOCK_LOG" \
    MOCK_ID_UID="${MOCK_ID_UID:-0}" \
    MOCK_AZATH_EXISTS="${MOCK_AZATH_EXISTS:-0}" \
    MOCK_AZATH_GROUP="${MOCK_AZATH_GROUP:-azath}" \
    MOCK_GROUP_EXISTS="${MOCK_GROUP_EXISTS:-0}" \
    MOCK_SERVICE_STATUS="${MOCK_SERVICE_STATUS:-4}" \
    MOCK_DAEMON_RELOAD_STATUS="${MOCK_DAEMON_RELOAD_STATUS:-0}" \
    MOCK_RESTART_STATUS="${MOCK_RESTART_STATUS:-0}" \
    MOCK_UNAME_MACHINE="${MOCK_UNAME_MACHINE:-x86_64}" \
    MOCK_404_BASENAME="${MOCK_404_BASENAME:-}" \
    MOCK_FIXTURE_DIR="${MOCK_FIXTURE_DIR:-}" \
    MOCK_CURL_TRANSPORT_EXIT="${MOCK_CURL_TRANSPORT_EXIT:-}" \
    MOCK_CURL_BLOCK="${MOCK_CURL_BLOCK:-0}" \
    MOCK_CURL_STARTED_FILE="${MOCK_CURL_STARTED_FILE:-}" \
    MOCK_MV_FAIL_DESTINATION="${MOCK_MV_FAIL_DESTINATION:-}" \
    MOCK_MKTEMP_DIR="$TEST_WORK/installer-tmp" \
    AZATH_VERSION="${AZATH_VERSION:-}" \
    /bin/sh "$INSTALLER" >"$STDOUT_FILE" 2>"$STDERR_FILE" &
  INSTALLER_PID=$!
}

assert_failure() {  if [ "$TEST_RC" -eq 0 ]; then
    fail "$1: expected failure"
  fi
}

assert_successful_preflight() {
  if ! grep -q '^curl' "$MOCK_LOG"; then
    fail "$1: expected curl after preflight"
  fi
}

assert_no_download_or_mutation() {
  if grep -Eq '^(curl|mktemp)' "$MOCK_LOG"; then
    fail "$1: expected no download or temporary-directory call, got $(cat "$MOCK_LOG")"
  fi
  if [ -e "$TEST_WORK/installer-tmp" ]; then
    fail "$1: expected no installer temporary directory"
  fi
  if [ -e /usr/local/bin/azath ] || [ -e /etc/azath ] || [ -e /usr/local/lib/systemd/system/azath.service ]; then
    fail "$1: expected no target-host mutation"
  fi
}

assert_stderr_contains() {
  if ! grep -Fq "$2" "$STDERR_FILE"; then
    fail "$1: expected stderr to contain $2"
  fi
}

assert_no_installation() {
  if [ -e /usr/local/bin/azath ] || [ -e /etc/azath ]; then
    fail "$1: mutated the host after validation failure"
  fi
  if [ -e "$TEST_WORK/installer-tmp" ]; then
    fail "$1: did not remove temporary data"
  fi
}

assert_no_binary_stage() {
  for stage_file in /usr/local/bin/.azath.*; do
    if [ -e "$stage_file" ]; then
      fail "$1: left staged binary $stage_file"
    fi
  done
}

assert_log_count() {
  actual_count=$(grep -Fc "$2" "$MOCK_LOG" || true)
  if [ "$actual_count" -ne "$3" ]; then
    fail "$1: expected $3 occurrences of $2, got $actual_count"
  fi
}

snapshot_dsm_operator_data() {
  snapshot_file=$1
  {
    sha256sum /etc/azath/client.yaml /etc/azath/secrets/homes.key.sealed /etc/azath/azath-unseal.sh /etc/azath/task-scheduler.marker
    stat -c '%n %u:%g %a' /etc/azath /etc/azath/secrets /etc/azath/client.yaml /etc/azath/secrets/homes.key.sealed /etc/azath/azath-unseal.sh /etc/azath/task-scheduler.marker
  } > "$snapshot_file"
}

begin_scenario() {
  reset_target_paths
  restore_platform_files
  setup_mocks
  MOCK_ID_UID=0
  MOCK_AZATH_EXISTS=0
  MOCK_AZATH_GROUP=azath
  MOCK_GROUP_EXISTS=0
  MOCK_SERVICE_STATUS=4
  MOCK_DAEMON_RELOAD_STATUS=0
  MOCK_RESTART_STATUS=0
  MOCK_UNAME_MACHINE=x86_64
  MOCK_404_BASENAME=
  MOCK_FIXTURE_DIR=
  MOCK_CURL_TRANSPORT_EXIT=
  MOCK_CURL_BLOCK=0
  MOCK_CURL_STARTED_FILE=
  MOCK_MV_FAIL_DESTINATION=
  AZATH_VERSION=
}

scenario_root_required() {
  begin_scenario
  MOCK_ID_UID=1000
  run_installer
  assert_failure root_required
  assert_no_download_or_mutation root_required
  assert_stderr_contains root_required 'run this installer as root'
}

scenario_invalid_version() {
  for requested_version in 'v1.2.3 bad' '../v1.2.3' '1.2.3' 'v1;echo'; do
    begin_scenario
    AZATH_VERSION=$requested_version
    run_installer
    assert_failure "invalid_version ($requested_version)"
    assert_no_download_or_mutation "invalid_version ($requested_version)"
    assert_stderr_contains "invalid_version ($requested_version)" 'AZATH_VERSION must be latest or a safe v-prefixed tag'
  done
}

scenario_debian13() {
  begin_scenario
  write_debian_release
  run_installer
  assert_failure debian13
  assert_successful_preflight debian13
}

scenario_dsm7() {
  begin_scenario
  write_dsm_release
  run_installer
  assert_failure dsm7
  assert_successful_preflight dsm7
}

scenario_ambiguous_platform() {
  begin_scenario
  write_ambiguous_release
  run_installer
  assert_failure ambiguous_platform
  assert_no_download_or_mutation ambiguous_platform
  assert_stderr_contains ambiguous_platform 'supported platforms are Debian 13 and Synology DSM 7'
}

scenario_unsupported_platform() {
  for release_writer in write_unsupported_debian_release write_unsupported_dsm_release write_missing_release; do
    begin_scenario
    "$release_writer"
    run_installer
    assert_failure "unsupported_platform ($release_writer)"
    assert_no_download_or_mutation "unsupported_platform ($release_writer)"
    assert_stderr_contains "unsupported_platform ($release_writer)" 'supported platforms are Debian 13 and Synology DSM 7'
  done
}

scenario_architecture_mapping() {
  for MOCK_UNAME_MACHINE in x86_64 aarch64; do
    begin_scenario
    write_debian_release
    run_installer
    assert_failure "architecture_mapping ($MOCK_UNAME_MACHINE)"
    assert_successful_preflight "architecture_mapping ($MOCK_UNAME_MACHINE)"
  done
}

scenario_unsupported_architecture() {
  begin_scenario
  write_debian_release
  MOCK_UNAME_MACHINE=ppc64le
  run_installer
  assert_failure unsupported_architecture
  assert_no_download_or_mutation unsupported_architecture
  assert_stderr_contains unsupported_architecture 'unsupported architecture: ppc64le'
}

scenario_missing_dependency() {
  begin_scenario
  write_debian_release
  rm -f "$MOCK_BIN/tar"
  run_installer
  assert_failure missing_dependency
  assert_no_download_or_mutation missing_dependency
  assert_stderr_contains missing_dependency 'tar is required but not installed'
}

scenario_dsm_without_server_commands() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'dsm_without_server_commands: expected successful installation'
  if grep -Eq 'systemctl|useradd|groupadd|getent' "$MOCK_LOG"; then
    fail 'dsm_without_server_commands: invoked a server-only command'
  fi
}

scenario_latest_stable_download() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  run_installer
  if [ "$TEST_RC" -ne 0 ]; then
    fail "latest_stable_download: expected verified release, got $(cat "$STDERR_FILE")"
  fi
  assert_log_contains latest_stable_download 'https://github.com/maruina/azath/releases/latest/download/checksums.txt'
  assert_log_contains latest_stable_download 'https://github.com/maruina/azath/releases/latest/download/azath_linux_amd64.tar.gz'
  if grep -Eq 'Authorization|api.github.com|python|jq' "$MOCK_LOG"; then
    fail 'latest_stable_download: used an unsupported download path'
  fi
}

scenario_checksum_missing() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  (
    cd "$MOCK_FIXTURE_DIR" || exit 1
    awk '$2 != "azath_linux_amd64.tar.gz"' checksums.txt > checksums.tmp
    mv checksums.tmp checksums.txt
  )
  run_installer
  assert_failure checksum_missing
  [ ! -e /usr/local/bin/azath ] || fail 'checksum_missing: installed a binary'
  [ ! -e /etc/azath ] || fail 'checksum_missing: created DSM scaffolding'
  assert_stderr_contains checksum_missing 'expected exactly one checksum'
}

scenario_checksum_duplicate() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  (
    cd "$MOCK_FIXTURE_DIR" || exit 1
    sha256sum azath_linux_amd64.tar.gz >> checksums.txt
  )
  run_installer
  assert_failure checksum_duplicate
  assert_no_installation checksum_duplicate
  assert_stderr_contains checksum_duplicate 'expected exactly one checksum'
}

scenario_checksum_mismatch() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  (
    cd "$MOCK_FIXTURE_DIR" || exit 1
    awk '$2 != "azath_linux_amd64.tar.gz"' checksums.txt > checksums.tmp
    printf '%064d  azath_linux_amd64.tar.gz\n' 0 >> checksums.tmp
    mv checksums.tmp checksums.txt
  )
  run_installer
  assert_failure checksum_mismatch
  assert_no_installation checksum_mismatch
  assert_stderr_contains checksum_mismatch 'checksum verification failed'
}

scenario_archive_malformed() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  printf 'not a gzip archive\n' > "$MOCK_FIXTURE_DIR/azath_linux_amd64.tar.gz"
  refresh_checksums
  run_installer
  assert_failure archive_malformed
  assert_no_installation archive_malformed
  assert_stderr_contains archive_malformed 'failed to extract azath'
}

scenario_archive_missing_binary() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  rebuild_stable_archives deploy/systemd/azath.service
  run_installer
  assert_failure archive_missing_binary
  assert_no_installation archive_missing_binary
  assert_stderr_contains archive_missing_binary 'failed to extract azath'
}

scenario_archive_missing_debian_unit() {
  begin_scenario
  write_debian_release
  create_release_fixtures v1.2.3
  rebuild_stable_archives azath
  run_installer
  assert_failure archive_missing_debian_unit
  assert_no_installation archive_missing_debian_unit
  assert_stderr_contains archive_missing_debian_unit 'failed to extract required Debian archive members'
}

scenario_candidate_not_executable() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  chmod 644 "$MOCK_FIXTURE_DIR/azath"
  rebuild_stable_archives azath deploy/systemd/azath.service
  run_installer
  assert_failure candidate_not_executable
  assert_no_installation candidate_not_executable
  assert_stderr_contains candidate_not_executable 'extracted azath binary is not executable'
}

scenario_candidate_version_failure() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  write_candidate v1.2.3 1
  rebuild_stable_archives azath deploy/systemd/azath.service
  run_installer
  assert_failure candidate_version_failure
  assert_no_installation candidate_version_failure
  assert_stderr_contains candidate_version_failure 'candidate azath --version failed'
}

scenario_cleanup_on_term() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'cleanup_on_term: baseline installation failed'
  create_release_fixtures v1.2.4
  setup_mocks
  MOCK_CURL_BLOCK=1
  MOCK_CURL_STARTED_FILE="$TEST_WORK/curl-started"
  run_installer_async
  wait_attempts=0
  while [ ! -s "$MOCK_CURL_STARTED_FILE" ] && [ "$wait_attempts" -lt 10 ]; do
    /bin/sleep 1
    wait_attempts=$((wait_attempts + 1))
  done
  if [ ! -s "$MOCK_CURL_STARTED_FILE" ]; then
    fail 'cleanup_on_term: download did not block'
  fi
  IFS=' ' read -r temporary_directory curl_pid < "$MOCK_CURL_STARTED_FILE" || fail 'cleanup_on_term: could not read download state'
  kill -TERM "$INSTALLER_PID"
  kill -TERM "$curl_pid"
  wait "$INSTALLER_PID" || TEST_RC=$?
  assert_failure cleanup_on_term
  [ ! -e "$temporary_directory" ] || fail 'cleanup_on_term: did not remove temporary directory'
  [ "$(/usr/local/bin/azath --version)" = 'v1.2.3 (commit: test)' ] || fail 'cleanup_on_term: changed installed binary'
}

scenario_pinned_stable_download() {  begin_scenario
  write_dsm_release
  AZATH_VERSION=v1.2.3
  create_release_fixtures v1.2.3
  run_installer
  [ "$TEST_RC" -ne 0 ] && fail "pinned_stable_download: expected success"
  assert_log_contains pinned_stable_download 'https://github.com/maruina/azath/releases/download/v1.2.3/checksums.txt'
  assert_log_contains pinned_stable_download 'https://github.com/maruina/azath/releases/download/v1.2.3/azath_linux_amd64.tar.gz'
  if grep -Fq 'azath_1.2.3_linux_amd64.tar.gz' "$MOCK_LOG"; then
    fail 'pinned_stable_download: requested legacy archive'
  fi
}

scenario_pinned_legacy_404() {
  begin_scenario
  write_dsm_release
  AZATH_VERSION=v0.1.0
  create_release_fixtures v0.1.0
  MOCK_404_BASENAME=azath_linux_amd64.tar.gz
  run_installer
  [ "$TEST_RC" -ne 0 ] && fail "pinned_legacy_404: expected success"
  assert_log_contains pinned_legacy_404 'azath_linux_amd64.tar.gz'
  assert_log_contains pinned_legacy_404 'azath_0.1.0_linux_amd64.tar.gz'
}

scenario_pinned_no_fallback_on_other_error() {
  begin_scenario
  write_debian_release
  AZATH_VERSION=v0.1.0
  create_release_fixtures v0.1.0
  MOCK_CURL_TRANSPORT_EXIT=7
  run_installer
  assert_failure pinned_no_fallback_on_other_error
  if grep -Fq 'azath_0.1.0_linux_amd64.tar.gz' "$MOCK_LOG"; then
    fail 'pinned_no_fallback_on_other_error: requested legacy archive'
  fi
}

scenario_dsm_fresh_install() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  run_installer
  [ "$TEST_RC" -ne 0 ] && fail 'dsm_fresh_install: expected success'
  [ -x /usr/local/bin/azath ] || fail 'dsm_fresh_install: missing executable'
  [ "$(stat -c '%a' /usr/local/bin/azath)" = 755 ] || fail 'dsm_fresh_install: binary mode is not 0755'
  for directory in /etc/azath /etc/azath/secrets; do
    [ "$(stat -c '%a' "$directory")" = 700 ] || fail "dsm_fresh_install: $directory mode is not 0700"
  done
  if grep -Eq 'systemctl|useradd|groupadd|getent' "$MOCK_LOG"; then
    fail 'dsm_fresh_install: invoked Debian-only command'
  fi
}

scenario_dsm_same_identity() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  run_installer
  first_inode=$(stat -c '%i' /usr/local/bin/azath)
  setup_mocks
  run_installer
  [ "$TEST_RC" -ne 0 ] && fail 'dsm_same_identity: expected success'
  [ "$(stat -c '%i' /usr/local/bin/azath)" = "$first_inode" ] || fail 'dsm_same_identity: replaced binary'
  if grep -Fq '/usr/local/bin/.azath.' "$MOCK_LOG"; then
    fail 'dsm_same_identity: staged a matching binary'
  fi
}

scenario_dsm_changed_identity() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  run_installer
  create_release_fixtures v1.2.4
  setup_mocks
  run_installer
  [ "$TEST_RC" -ne 0 ] && fail 'dsm_changed_identity: expected success'
  if [ "$(/usr/local/bin/azath --version)" != 'v1.2.4 (commit: test)' ]; then
    fail 'dsm_changed_identity: binary identity did not change'
  fi
  assert_log_contains dsm_changed_identity '/usr/local/bin/.azath.'
}

scenario_rename_failure_preserves_binary() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'rename_failure_preserves_binary: baseline installation failed'
  create_release_fixtures v1.2.4
  setup_mocks
  MOCK_MV_FAIL_DESTINATION=/usr/local/bin/azath
  run_installer
  assert_failure rename_failure_preserves_binary
  [ "$(/usr/local/bin/azath --version)" = 'v1.2.3 (commit: test)' ] || fail 'rename_failure_preserves_binary: replaced the old binary'
  assert_no_binary_stage rename_failure_preserves_binary
  assert_stderr_contains rename_failure_preserves_binary 'failed to replace azath binary'
}

scenario_dsm_preserves_operator_files() {
  begin_scenario
  write_dsm_release
  create_release_fixtures v1.2.3
  mkdir -p /etc/azath/secrets
  printf 'endpoint: https://azath.example\n' > /etc/azath/client.yaml
  printf 'sealed blob\n' > /etc/azath/secrets/homes.key.sealed
  printf '#!/bin/sh\necho operator helper\n' > /etc/azath/azath-unseal.sh
  printf 'Task Scheduler: operator-owned\n' > /etc/azath/task-scheduler.marker
  chmod 751 /etc/azath
  chmod 711 /etc/azath/secrets
  chmod 640 /etc/azath/client.yaml /etc/azath/secrets/homes.key.sealed
  chmod 700 /etc/azath/azath-unseal.sh /etc/azath/task-scheduler.marker
  snapshot_dsm_operator_data "$TEST_WORK/operator-before"

  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'dsm_preserves_operator_files: initial installation failed'
  snapshot_dsm_operator_data "$TEST_WORK/operator-after-first"
  cmp -s "$TEST_WORK/operator-before" "$TEST_WORK/operator-after-first" || fail 'dsm_preserves_operator_files: changed data during initial installation'

  setup_mocks
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'dsm_preserves_operator_files: same-identity installation failed'
  snapshot_dsm_operator_data "$TEST_WORK/operator-after-same"
  cmp -s "$TEST_WORK/operator-before" "$TEST_WORK/operator-after-same" || fail 'dsm_preserves_operator_files: changed data during same-identity installation'

  create_release_fixtures v1.2.4
  setup_mocks
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'dsm_preserves_operator_files: upgrade failed'
  snapshot_dsm_operator_data "$TEST_WORK/operator-after-upgrade"
  cmp -s "$TEST_WORK/operator-before" "$TEST_WORK/operator-after-upgrade" || fail 'dsm_preserves_operator_files: changed data during upgrade'
  [ "$(/usr/local/bin/azath --version)" = 'v1.2.4 (commit: test)' ] || fail 'dsm_preserves_operator_files: did not replace changed binary'
}

scenario_debian_fresh_scaffolding() {  begin_scenario
  write_debian_release
  create_release_fixtures v1.2.3
  run_installer
  [ "$TEST_RC" -ne 0 ] && fail 'debian_fresh_scaffolding: expected success'
  [ -x /usr/local/bin/azath ] || fail 'debian_fresh_scaffolding: missing binary'
  [ -f /usr/local/lib/systemd/system/azath.service ] || fail 'debian_fresh_scaffolding: missing unit'
  [ "$(stat -c '%a' /etc/azath)" = 700 ] || fail 'debian_fresh_scaffolding: config mode is not 0700'
  assert_log_contains debian_fresh_scaffolding groupadd
  assert_log_contains debian_fresh_scaffolding useradd
  assert_log_contains debian_fresh_scaffolding 'systemctl <daemon-reload>'
  if grep -Eq 'systemctl <(restart|start|enable)' "$MOCK_LOG"; then
    fail 'debian_fresh_scaffolding: started or enabled service'
  fi
}

scenario_debian_existing_account_directory() {
  begin_scenario
  write_debian_release
  create_release_fixtures v1.2.3
  mkdir -p /etc/azath
  printf 'operator configuration\n' > /etc/azath/server.yaml
  chmod 751 /etc/azath
  chmod 640 /etc/azath/server.yaml
  directory_metadata_before=$(stat -c '%u:%g:%a' /etc/azath)
  file_metadata_before=$(stat -c '%u:%g:%a' /etc/azath/server.yaml)
  directory_hash_before=$(sha256sum /etc/azath/server.yaml)
  MOCK_AZATH_EXISTS=1
  MOCK_AZATH_GROUP=operators
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'debian_existing_account_directory: expected success'
  [ "$(stat -c '%u:%g:%a' /etc/azath)" = "$directory_metadata_before" ] || fail 'debian_existing_account_directory: changed directory metadata'
  [ "$(stat -c '%u:%g:%a' /etc/azath/server.yaml)" = "$file_metadata_before" ] || fail 'debian_existing_account_directory: changed file metadata'
  [ "$(sha256sum /etc/azath/server.yaml)" = "$directory_hash_before" ] || fail 'debian_existing_account_directory: changed directory contents'
  if grep -Eq '^(getent|groupadd|useradd)' "$MOCK_LOG"; then
    fail 'debian_existing_account_directory: mutated existing account'
  fi
}

scenario_debian_operator_unit_preserved() {
  begin_scenario
  write_debian_release
  create_release_fixtures v1.2.3
  mkdir -p /etc/systemd/system/azath.service.d
  printf '[Service]\nEnvironment=OPERATOR=1\n' > /etc/systemd/system/azath.service
  printf '[Service]\nEnvironment=DROP_IN=1\n' > /etc/systemd/system/azath.service.d/10-local.conf
  printf '[Service]\nEnvironment=LINK_TARGET=1\n' > /etc/systemd/system/azath-operator.conf
  ln -s /etc/systemd/system/azath-operator.conf /etc/systemd/system/azath.service.d/20-linked.conf
  sha256sum /etc/systemd/system/azath.service /etc/systemd/system/azath.service.d/10-local.conf /etc/systemd/system/azath-operator.conf > "$TEST_WORK/operator-layer-before"
  stat -c '%n %u:%g %a' /etc/systemd/system/azath.service /etc/systemd/system/azath.service.d /etc/systemd/system/azath.service.d/10-local.conf /etc/systemd/system/azath.service.d/20-linked.conf > "$TEST_WORK/operator-layer-metadata-before"
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'debian_operator_unit_preserved: expected success'
  sha256sum /etc/systemd/system/azath.service /etc/systemd/system/azath.service.d/10-local.conf /etc/systemd/system/azath-operator.conf > "$TEST_WORK/operator-layer-after"
  stat -c '%n %u:%g %a' /etc/systemd/system/azath.service /etc/systemd/system/azath.service.d /etc/systemd/system/azath.service.d/10-local.conf /etc/systemd/system/azath.service.d/20-linked.conf > "$TEST_WORK/operator-layer-metadata-after"
  cmp -s "$TEST_WORK/operator-layer-before" "$TEST_WORK/operator-layer-after" || fail 'debian_operator_unit_preserved: changed operator-owned contents'
  cmp -s "$TEST_WORK/operator-layer-metadata-before" "$TEST_WORK/operator-layer-metadata-after" || fail 'debian_operator_unit_preserved: changed operator-owned metadata'
  [ "$(readlink /etc/systemd/system/azath.service.d/20-linked.conf)" = /etc/systemd/system/azath-operator.conf ] || fail 'debian_operator_unit_preserved: changed operator symlink'

  rm -f /etc/systemd/system/azath.service
  ln -s /dev/null /etc/systemd/system/azath.service
  setup_mocks
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'debian_operator_unit_preserved: masked rerun failed'
  [ "$(readlink /etc/systemd/system/azath.service)" = /dev/null ] || fail 'debian_operator_unit_preserved: changed operator mask'
}

scenario_debian_same_unit_no_reload() {
  begin_scenario
  write_debian_release
  create_release_fixtures v1.2.3
  mkdir -p /usr/local/lib/systemd/system
  cp "$MOCK_FIXTURE_DIR/deploy/systemd/azath.service" /usr/local/lib/systemd/system/azath.service
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'debian_same_unit_no_reload: expected success'
  assert_log_count debian_same_unit_no_reload 'systemctl <daemon-reload>' 0
}

scenario_debian_changed_unit_reload_once() {
  begin_scenario
  write_debian_release
  create_release_fixtures v1.2.3
  mkdir -p /usr/local/lib/systemd/system
  printf '[Service]\nDescription=old\n' > /usr/local/lib/systemd/system/azath.service
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'debian_changed_unit_reload_once: expected success'
  assert_log_count debian_changed_unit_reload_once 'systemctl <daemon-reload>' 1
  cmp -s "$MOCK_FIXTURE_DIR/deploy/systemd/azath.service" /usr/local/lib/systemd/system/azath.service || fail 'debian_changed_unit_reload_once: did not replace managed unit'
}

scenario_daemon_reload_failure_preserves_binary() {
  begin_scenario
  write_debian_release
  create_release_fixtures v1.2.3
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'daemon_reload_failure_preserves_binary: baseline installation failed'
  write_candidate v1.2.4
  printf '[Service]\nDescription=changed\n' > "$MOCK_FIXTURE_DIR/deploy/systemd/azath.service"
  rebuild_stable_archives azath deploy/systemd/azath.service
  setup_mocks
  MOCK_AZATH_EXISTS=1
  MOCK_DAEMON_RELOAD_STATUS=1
  run_installer
  assert_failure daemon_reload_failure_preserves_binary
  [ "$(/usr/local/bin/azath --version)" = 'v1.2.3 (commit: test)' ] || fail 'daemon_reload_failure_preserves_binary: replaced the old binary'
  assert_no_binary_stage daemon_reload_failure_preserves_binary
  assert_stderr_contains daemon_reload_failure_preserves_binary 'systemd daemon reload failed; binary activation did not begin'
}

scenario_debian_legacy_existing_unit() {
  begin_scenario
  write_debian_release
  AZATH_VERSION=v0.1.0
  create_release_fixtures v0.1.0
  mkdir -p /usr/local/lib/systemd/system
  printf '[Service]\nDescription=operator unit\n' > /usr/local/lib/systemd/system/azath.service
  sha256sum /usr/local/lib/systemd/system/azath.service > "$TEST_WORK/managed-unit-before"
  MOCK_404_BASENAME=azath_linux_amd64.tar.gz
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'debian_legacy_existing_unit: expected success'
  sha256sum /usr/local/lib/systemd/system/azath.service > "$TEST_WORK/managed-unit-after"
  cmp -s "$TEST_WORK/managed-unit-before" "$TEST_WORK/managed-unit-after" || fail 'debian_legacy_existing_unit: changed existing unit'
}

scenario_debian_legacy_fresh_warning() {
  begin_scenario
  write_debian_release
  AZATH_VERSION=v0.1.0
  create_release_fixtures v0.1.0
  MOCK_404_BASENAME=azath_linux_amd64.tar.gz
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'debian_legacy_fresh_warning: expected success'
  [ ! -e /usr/local/lib/systemd/system/azath.service ] || fail 'debian_legacy_fresh_warning: installed a unit from a legacy archive'
  assert_stderr_contains debian_legacy_fresh_warning 'predates managed systemd packaging; no unit was installed'
}

scenario_debian_active_upgrade() {  begin_scenario
  write_debian_release
  create_release_fixtures v1.2.3
  run_installer
  create_release_fixtures v1.2.4
  setup_mocks
  MOCK_AZATH_EXISTS=1
  MOCK_SERVICE_STATUS=0
  run_installer
  [ "$TEST_RC" -ne 0 ] && fail 'debian_active_upgrade: expected success'
  [ "$(/usr/local/bin/azath --version)" = 'v1.2.4 (commit: test)' ] || fail 'debian_active_upgrade: binary did not change'
  assert_log_contains debian_active_upgrade 'systemctl <is-active> <azath.service>'
  assert_log_count debian_active_upgrade 'systemctl <restart> <azath.service>' 1
  if ! awk '/systemctl <is-active> <azath.service>/{state_line=NR} /systemctl <restart> <azath.service>/{restart_line=NR} END { exit !(state_line && restart_line && state_line < restart_line) }' "$MOCK_LOG"; then
    fail 'debian_active_upgrade: did not query service state before restart'
  fi
}

scenario_debian_inactive_upgrade() {
  begin_scenario
  write_debian_release
  create_release_fixtures v1.2.3
  run_installer
  create_release_fixtures v1.2.4
  setup_mocks
  MOCK_AZATH_EXISTS=1
  MOCK_SERVICE_STATUS=3
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'debian_inactive_upgrade: expected success'
  [ "$(/usr/local/bin/azath --version)" = 'v1.2.4 (commit: test)' ] || fail 'debian_inactive_upgrade: did not replace binary'
  if grep -Eq 'systemctl <(restart|start|enable)' "$MOCK_LOG"; then
    fail 'debian_inactive_upgrade: activated an inactive service'
  fi
}

scenario_debian_absent_upgrade() {
  begin_scenario
  write_debian_release
  create_release_fixtures v1.2.3
  run_installer
  create_release_fixtures v1.2.4
  setup_mocks
  MOCK_AZATH_EXISTS=1
  MOCK_SERVICE_STATUS=4
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'debian_absent_upgrade: expected success'
  [ "$(/usr/local/bin/azath --version)" = 'v1.2.4 (commit: test)' ] || fail 'debian_absent_upgrade: did not replace binary'
  if grep -Eq 'systemctl <(restart|start|enable)' "$MOCK_LOG"; then
    fail 'debian_absent_upgrade: activated an absent service'
  fi
}

scenario_debian_restart_failure() {
  begin_scenario
  write_debian_release
  create_release_fixtures v1.2.3
  run_installer
  create_release_fixtures v1.2.4
  setup_mocks
  MOCK_AZATH_EXISTS=1
  MOCK_SERVICE_STATUS=0
  MOCK_RESTART_STATUS=1
  run_installer
  assert_failure debian_restart_failure
  [ "$(/usr/local/bin/azath --version)" = 'v1.2.4 (commit: test)' ] || fail 'debian_restart_failure: did not retain new binary'
  assert_log_contains debian_restart_failure 'systemctl <status> <azath.service> <--no-pager>'
  assert_stderr_contains debian_restart_failure 'installation partially succeeded: installed v1.2.4 (commit: test); previous identity was v1.2.3 (commit: test)'
  assert_stderr_contains debian_restart_failure 'AZATH_VERSION=v1.2.3 sh'
}

scenario_debian_same_identity_active() {
  begin_scenario
  write_debian_release
  create_release_fixtures v1.2.3
  run_installer
  setup_mocks
  MOCK_AZATH_EXISTS=1
  MOCK_SERVICE_STATUS=0
  run_installer
  [ "$TEST_RC" -eq 0 ] || fail 'debian_same_identity_active: expected success'
  if grep -Eq 'systemctl <(is-active|restart)' "$MOCK_LOG"; then
    fail 'debian_same_identity_active: queried or restarted an unchanged service'
  fi
}

scenario_debian_indeterminate_state() {
  begin_scenario
  write_debian_release
  create_release_fixtures v1.2.3
  run_installer
  create_release_fixtures v1.2.4
  setup_mocks
  MOCK_AZATH_EXISTS=1
  MOCK_SERVICE_STATUS=1
  run_installer
  assert_failure debian_indeterminate_state
  [ "$(/usr/local/bin/azath --version)" = 'v1.2.3 (commit: test)' ] || fail 'debian_indeterminate_state: replaced binary'
  assert_stderr_contains debian_indeterminate_state 'could not determine azath.service state'
}

assert_log_contains() {
  if ! grep -Fq "$2" "$MOCK_LOG"; then
    fail "$1: expected mock log to contain $2"
  fi
}

run_scenario() {
  scenario_name=$1
  failures_before=$FAIL_COUNT
  "scenario_$scenario_name"
  if [ "$FAIL_COUNT" -eq "$failures_before" ]; then
    pass "$scenario_name"
  fi
}

main() {
  require_test_container
  TEST_WORK=$(mktemp -d)
  save_platform_files
  trap cleanup EXIT HUP INT TERM

  if [ "$#" -eq 0 ]; then
    set -- root_required invalid_version debian13 dsm7 ambiguous_platform unsupported_platform architecture_mapping unsupported_architecture missing_dependency dsm_without_server_commands latest_stable_download checksum_missing checksum_duplicate checksum_mismatch archive_malformed archive_missing_binary archive_missing_debian_unit candidate_not_executable candidate_version_failure cleanup_on_term pinned_stable_download pinned_legacy_404 pinned_no_fallback_on_other_error dsm_fresh_install dsm_same_identity dsm_changed_identity rename_failure_preserves_binary dsm_preserves_operator_files debian_fresh_scaffolding debian_existing_account_directory debian_operator_unit_preserved debian_same_unit_no_reload debian_changed_unit_reload_once daemon_reload_failure_preserves_binary debian_legacy_existing_unit debian_legacy_fresh_warning debian_active_upgrade debian_inactive_upgrade debian_absent_upgrade debian_restart_failure debian_same_identity_active debian_indeterminate_state
  fi

  for scenario_name in "$@"; do
    case "$scenario_name" in
      root_required|invalid_version|debian13|dsm7|ambiguous_platform|unsupported_platform|architecture_mapping|unsupported_architecture|missing_dependency|dsm_without_server_commands|latest_stable_download|checksum_missing|checksum_duplicate|checksum_mismatch|archive_malformed|archive_missing_binary|archive_missing_debian_unit|candidate_not_executable|candidate_version_failure|cleanup_on_term|pinned_stable_download|pinned_legacy_404|pinned_no_fallback_on_other_error|dsm_fresh_install|dsm_same_identity|dsm_changed_identity|rename_failure_preserves_binary|dsm_preserves_operator_files|debian_fresh_scaffolding|debian_existing_account_directory|debian_operator_unit_preserved|debian_same_unit_no_reload|debian_changed_unit_reload_once|daemon_reload_failure_preserves_binary|debian_legacy_existing_unit|debian_legacy_fresh_warning|debian_active_upgrade|debian_inactive_upgrade|debian_absent_upgrade|debian_restart_failure|debian_same_identity_active|debian_indeterminate_state)
        run_scenario "$scenario_name"
        ;;
      *)
        printf 'unknown scenario: %s\n' "$scenario_name" >&2
        exit 2
        ;;
    esac
  done

  printf '%s passed, %s failed\n' "$PASS_COUNT" "$FAIL_COUNT"
  [ "$FAIL_COUNT" -eq 0 ]
}

main "$@"
