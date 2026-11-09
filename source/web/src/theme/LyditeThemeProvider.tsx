import { ThemeProvider } from "@vipengele/react-tokens";
import type { ReactNode } from "react";
import "./fonts";
import { colorMode, lyditeTheme, pageStylesheet } from "./theme";

export interface LyditeThemeProviderProps {
  children?: ReactNode;
}

/** Paints its subtree in lydite's dark theme, the only mode the dashboard has. */
export function LyditeThemeProvider({ children }: LyditeThemeProviderProps) {
  return (
    <ThemeProvider theme={lyditeTheme} colorMode={colorMode}>
      <style href="lydite-page" precedence="default">
        {pageStylesheet}
      </style>
      {children}
    </ThemeProvider>
  );
}
