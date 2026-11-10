import { Vipengele } from "@vipengele/react";
import type { ReactNode } from "react";
import "./fonts";
import { colorMode, lyditeTheme, pageStylesheet } from "./theme";

/** The telemetry scope tag every span and event the dashboard emits is recorded under. */
export const scopeTag = "lydite-dashboard";

export interface LyditeRootProps {
  children?: ReactNode;
}

/**
 * Mounts the dashboard under vipengele's app-root provider with lydite's configuration: its
 * theme pinned to the dark mode, the only one the dashboard has, its telemetry scope, and the
 * page-level stylesheet and self-hosted fonts a `Theme` cannot carry.
 */
export function LyditeRoot({ children }: LyditeRootProps) {
  return (
    <Vipengele theme={lyditeTheme} colorMode={colorMode} scope={{ tag: scopeTag }}>
      <style href="lydite-page" precedence="default">
        {pageStylesheet}
      </style>
      {children}
    </Vipengele>
  );
}
