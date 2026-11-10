import { Card, Stack, Typography } from "@vipengele/react-ui";
import { LyditeRoot } from "./theme/LyditeRoot";

export function App() {
  return (
    <LyditeRoot>
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
    </LyditeRoot>
  );
}
