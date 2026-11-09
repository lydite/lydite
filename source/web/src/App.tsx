import { Card, Stack, Typography } from "@vipengele/react-ui";
import { LyditeThemeProvider } from "./theme/LyditeThemeProvider";

export function App() {
  return (
    <LyditeThemeProvider>
      <Stack as="main" gap="space-5" style={{ padding: "var(--vpg-space-8)" }}>
        <Typography variant="h1">lydite</Typography>
        <Card>
          <Card.Content>
            <Typography variant="body-md" color="secondary">
              quality history
            </Typography>
          </Card.Content>
        </Card>
      </Stack>
    </LyditeThemeProvider>
  );
}
