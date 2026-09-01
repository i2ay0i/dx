/// <reference types="@raycast/api">

/* 🚧 🚧 🚧
 * This file is auto-generated from the extension's manifest.
 * Do not modify manually. Instead, update the `package.json` file.
 * 🚧 🚧 🚧 */

/* eslint-disable @typescript-eslint/ban-types */

type ExtensionPreferences = {
  /** dx binary path - Path to the dx CLI */
  "dxPath": string,
  /** Remote hosts - Comma-separated ssh hosts whose dx services to also list (e.g. nano). dx must be installed on each host. */
  "remoteHosts": string
}

/** Preferences accessible in all the extension's commands */
declare type Preferences = ExtensionPreferences

declare namespace Preferences {
  /** Preferences accessible in the `list-services` command */
  export type ListServices = ExtensionPreferences & {}
}

declare namespace Arguments {
  /** Arguments passed to the `list-services` command */
  export type ListServices = {}
}

