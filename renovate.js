module.exports = {
  $schema: "https://docs.renovatebot.com/renovate-schema.json",
  extends: [
    "config:best-practices",
    "default:automergeMinor",
    "helpers:pinGitHubActionDigests",
  ],
  gitAuthor: "renovate[bot] <29139614+renovate[bot]@users.noreply.github.com>",
  semanticCommits: "enabled",
  labels: ["dependencies"],
  rebaseWhen: "conflicted",
  rangeStrategy: "bump",
  minimumReleaseAgeBehaviour: "creation-date-not-required",
  vulnerabilityAlerts: {
    enabled: true,
  },
  packageRules: [
    // Auto-merge patch updates after 7 days
    {
      matchUpdateTypes: ["patch"],
      minimumReleaseAge: "7 days",
      automerge: true,
    },
    // Auto-merge minor Go dependency updates after 14 days
    {
      matchUpdateTypes: ["minor"],
      matchManagers: ["gomod"],
      minimumReleaseAge: "14 days",
      automerge: true,
    },
    // Major updates always require manual review
    {
      matchUpdateTypes: ["major"],
      automerge: false,
    },
  ],
  customManagers: [
    // Track Renovate version pinned in the Renovate workflow
    {
      customType: "regex",
      managerFilePatterns: [".github/workflows/renovate.yml"],
      matchStrings: ["RENOVATE_VERSION:\\s*\"?(?<currentValue>[^\"\\n]+)\"?"],
      depNameTemplate: "renovate",
      datasourceTemplate: "npm",
    },
    // Track Node.js version pinned in the Renovate workflow
    {
      customType: "regex",
      managerFilePatterns: [".github/workflows/renovate.yml"],
      matchStrings: ["NODE_VERSION:\\s*\"?(?<currentValue>[^\"\\n]+)\"?"],
      depNameTemplate: "node",
      datasourceTemplate: "node-version",
    },
    // Track golangci-lint version pinned in CI workflow
    {
      customType: "regex",
      managerFilePatterns: [".github/workflows/ci.yml"],
      matchStrings: ["version:\\s*v?(?<currentValue>[0-9]+\\.[0-9]+\\.[0-9]+)"],
      depNameTemplate: "golangci/golangci-lint",
      datasourceTemplate: "github-releases",
      extractVersionTemplate: "^v(?<version>.*)$",
    },
    // Track golangci-lint SHA pin in ci.yml (golangci-lint-action)
    {
      customType: "regex",
      managerFilePatterns: [".github/workflows/ci.yml"],
      matchStrings: [
        "golangci/golangci-lint-action@(?<currentDigest>[a-f0-9]+)\\s*#\\s*(?<currentValue>v[^\\s]+)",
      ],
      depNameTemplate: "golangci/golangci-lint-action",
      datasourceTemplate: "github-tags",
      versioningTemplate: "semver",
    },
  ],
};
