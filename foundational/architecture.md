> Superseded OCI note: the implementation uses CNCF Distribution's Docker Hub
> pull-through cache, stored under `registry/`, rather than Zot. The remainder
> of this historical planning document records the original design discussion.

Here’s the updated implementation task breakdown incorporating the **single portable binary**, **service container**, **supervisord**, optional **crond**, host-side Git cache, and disposable-container model.

1. **Define the product boundary and persistent layout**
   - The only user-facing executable is `swe-cache`.
   - `swe-cache` owns configuration, lifecycle, health checks, Git cache operations, maintenance, and diagnostics.
   - Persistent data lives outside Docker, e.g.:

     ```text
     /var/lib/swe-cache/
     ├── apt/
     ├── zot/
     ├── git/
     ├── config/
     └── logs/
     ```

   - `/var/lib/docker` is explicitly disposable.
   - Define permissions, ownership, defaults, and override mechanisms.

2. **Create the `swe-cache` binary skeleton**
   - Prefer Go for a portable single executable.
   - Establish CLI structure:

     ```text
     swe-cache start
     swe-cache stop
     swe-cache restart
     swe-cache status
     swe-cache doctor
     swe-cache stats
     swe-cache gc
     swe-cache git ...
     ```

   - Add version reporting and structured exit codes.
   - Keep the initial binary independent of Docker SDK libraries where practical.

3. **Define the service-container image**
   - Build one versioned image containing:
     - `apt-cacher-ng`
     - `zot`
     - `supervisord`
     - `crond`/`cron`, if maintenance requires it
     - certificates and minimal runtime dependencies

   - `supervisord` runs as PID 1.
   - The image itself contains no valuable persistent state.
   - Pin and test component versions.

4. **Configure supervisord**
   - Manage:

     ```text
     apt-cacher-ng
     zot
     crond   # optional
     ```

   - Both cache services should run in foreground mode where supported.
   - Configure automatic restart behavior.
   - Route logs predictably.
   - Ensure one failed service does not silently make the overall container appear healthy.

5. **Implement `swe-cache start`**
   - Detect Docker availability.
   - Resolve/create cache directories.
   - Generate runtime configuration.
   - Ensure the matching service image exists.
   - Pull it if necessary.
   - Start the service container with bind mounts.
   - Publish only required ports.
   - Make repeated `start` calls idempotent.

6. **Implement `stop`, `restart`, and container recreation**
   - `stop` stops services without deleting cache contents.
   - `restart` safely recreates services.
   - Support replacing the service container while retaining:

     ```text
     apt/
     zot/
     git/
     ```

   - Treat the service container itself as disposable.

7. **Version the binary and service image together**
   - Example:

     ```text
     swe-cache 0.3.0
     ↕
     ghcr.io/.../swe-cache-services:0.3.0
     ```

   - Define compatibility rules.
   - Avoid arbitrary combinations of binary and container versions.
   - Add upgrade behavior for configuration schema changes.

8. **Implement configuration generation**
   - The binary generates:

     ```text
     apt-cacher-ng configuration
     zot configuration
     supervisord configuration
     cron configuration, if enabled
     ```

   - Avoid requiring users to manually edit service-specific config files.
   - Optionally support one top-level config such as:

     ```toml
     cache_root = "/var/lib/swe-cache"

     [apt]
     enabled = true
     port = 3142

     [oci]
     enabled = true
     port = 5000

     [git]
     enabled = true

     [maintenance]
     enabled = false
     ```

9. **Integrate the existing apt-cacher-ng setup**
   - Since apt-cacher-ng already exists in the environment, decide whether:
     - `swe-cache` adopts/manages that existing instance, or
     - the final packaged solution always runs its own containerized instance.

   - Preserve any useful existing cache data if migrating.
   - Bind persistent APT data into the service container.
   - Validate metadata refresh and `.deb` caching behavior.

10. **Configure zot pull-through caching**
    - Store zot state under:

      ```text
      /var/lib/swe-cache/zot
      ```

- Configure Docker Hub first.
- Then add required upstreams such as:

  ```text
  ghcr.io
  quay.io
  mcr.microsoft.com
  registry.k8s.io
  public.ecr.aws
  ```

- Preserve digest semantics.
- Define authentication behavior for private registries separately.

11. **Integrate Docker with zot**

- Configure Docker/containerd to use the local cache where supported.
- Keep ordinary evaluator commands unchanged:

  ```bash
  docker pull ubuntu:24.04
  ```

- Handle registry-specific mirror configuration where necessary.
- Provide clear diagnostics when a registry cannot be transparently mirrored.

12. **Implement `swe-cache status`**

- Report:

  ```text
  service container state
  apt-cacher-ng state
  zot state
  optional cron state
  endpoints
  persistent cache paths
  binary/service-image versions
  ```

- Distinguish “container running” from “services healthy”.

13. **Implement `swe-cache doctor`**

- Validate:
  - Docker installed/running
  - cache directories writable
  - required ports available
  - service container reachable
  - apt-cacher-ng responds
  - zot responds
  - Git executable available
  - upstream connectivity where appropriate
  - Docker mirror configuration is correct

- Produce actionable failures rather than raw subprocess output.

14. **Implement host-side Git mirror storage**

- Keep Git outside the service container.
- Normalize repository URLs into deterministic mirror paths:

  ```text
  /var/lib/swe-cache/git/github.com/pixijs/pixijs.git
  ```

- Use bare mirrors.
- Do not require a long-running Git server.

15. **Implement `swe-cache git clone`**

- Example:

  ```bash
  swe-cache git clone \
    https://github.com/pixijs/pixijs.git \
    ./pixijs
  ```

- On first use:

  ```text
  git clone --mirror upstream → cache
  ```

- On subsequent use:

  ```text
  local mirror → destination
  ```

- Preserve the real upstream URL as `origin` where appropriate.

16. **Implement commit-aware Git caching**

- Support:

  ```bash
  swe-cache git clone \
    --commit <sha> \
    <repo> \
    <destination>
  ```

- Before contacting GitHub:

  ```bash
  git cat-file -e "<sha>^{commit}"
  ```

- If present, perform zero upstream access.
- If absent, fetch only what is necessary.
- Optimize specifically for historical SWE-bench commits.

17. **Define Git clone strategy**

- Decide between:
  - local hardlinked clone
  - alternates/reference clone
  - fully independent clone

- Favor fast local cloning while avoiding fragile long-lived dependencies.
- Ensure temporary evaluator workspaces remain safe while mirrors are maintained.

18. **Add Git locking and concurrency controls**

- Prevent simultaneous first-time creation of the same mirror.
- Serialize conflicting fetch/repack operations per repository.
- Do not globally serialize unrelated repositories.
- Recover cleanly from interrupted mirror creation.

19. **Define Git maintenance policy**

- Repack when useful.
- Avoid aggressive pruning of historical objects.
- Never let maintenance remove commits still useful to benchmarks.
- Consider Git LFS separately rather than bundling it into the first version.

20. **Decide whether crond is actually required**

- First prefer maintenance initiated by `swe-cache`, for example:

  ```text
  swe-cache start
    → run overdue lightweight maintenance
  ```

- Use `crond` only if cache services need independent periodic work.
- If needed, make it a third supervisord-managed service.
- Keep maintenance jobs idempotent.

21. **Implement `swe-cache gc`**

- Manage each cache independently:

  ```text
  swe-cache gc apt
  swe-cache gc oci
  swe-cache gc git
  swe-cache gc --all
  ```

- Define safe defaults.
- Prefer retention by age/size over destructive full wipes.
- Never make ordinary GC equivalent to “delete everything”.

22. **Implement cache size limits**

- Support individual budgets:

  ```text
  apt: 20 GB
  zot: 100 GB
  git: 50 GB
  ```

- Optionally enforce a global cache budget.
- Prevent one subsystem from exhausting the disk.

23. **Implement `swe-cache stats`**

- Report at least:

  ```text
  cache disk usage
  number of Git mirrors
  upstream vs local Git clone behavior
  APT cache usage
  OCI cache usage
  service uptime
  ```

- Add hit/miss and downloaded-byte metrics where underlying services expose them reliably.

24. **Add service health checks**

- Test apt-cacher-ng endpoint.
- Test zot OCI endpoint.
- Validate supervisord process status.
- Surface degraded states such as:

  ```text
  apt healthy
  zot unhealthy
  cron disabled
  ```

25. **Handle bootstrap behavior**

- Accept that the initial service-image pull cannot use zot.
- Keep the service image reasonably small.
- Do not embed the Docker image into the binary initially.
- After startup, normal OCI traffic can use the cache.

26. **Implement safe configuration of evaluator containers**

- Inject the apt-cacher-ng proxy automatically.
- Configure access to host-published cache endpoints.
- Avoid modifying individual project install scripts.
- Make cache bypass possible for troubleshooting.

27. **Define failure and fallback behavior**

- If apt-cacher-ng is unavailable, decide whether APT should fail or bypass.
- If zot is unavailable, allow direct registry access when policy permits.
- If a Git mirror is corrupted, quarantine/rebuild it.
- Cache failures should not silently corrupt evaluation results.

28. **Test container destruction scenarios**

- Delete the service container and recreate it.
- Delete all Docker images.
- Delete Docker build cache.
- Replace `/var/lib/docker`.
- Confirm:

  ```text
  APT cache survives
  zot cache survives
  Git mirrors survive
  ```

29. **Test cold/warm behavior**

- Cold:

  ```text
  GitHub + registry + apt upstream used
  ```

- Warm:

  ```text
  Git clone local
  OCI blobs local
  APT packages local
  ```

- Partially warm:
  validate each cache independently.

30. **Benchmark the complete system**

- Measure:
  - first Git clone vs repeated clone
  - first image pull vs pull after Docker purge
  - first apt install vs repeated install
  - full evaluator startup time
  - upstream bytes transferred

- Use the measurements to prioritize further work.

31. **Package the binary for portability**

- Build release binaries for required architectures.
- Prefer minimal runtime dependencies beyond:

  ```text
  Docker
  Git
  ```

- Provide checksums/signatures as appropriate.
- Keep installation to essentially copying one executable.

32. **Only after the base system is stable, consider extensions**

- Podman support.
- Git LFS caching.
- BuildKit cache persistence/export.
- Cache prewarming.
- Remote/shared caches across machines.
- Backup/restore.
- Metrics export.
- Multi-user permissions.
- Native Docker API integration instead of shelling out.

I’d group those into five milestones:

```text
Milestone 1
Portable binary + service image + supervisord + start/stop/status

Milestone 2
apt-cacher-ng + zot fully operational and persistent

Milestone 3
Git bare-mirror cache + commit-aware swe-cache git clone

Milestone 4
doctor + stats + maintenance + GC + concurrency + recovery

Milestone 5
Evaluator integration + destructive-cache tests + benchmarking
```

That sequencing gets the core architecture working early without prematurely building maintenance, metrics, or advanced cache features before the three caching paths themselves are proven.
