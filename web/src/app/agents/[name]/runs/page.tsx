import { AgentRunsPage } from "@/components/agent-runs-page";

type RouteProps = {
  params: Promise<{
    name: string;
  }>;
};

export default async function AgentRunsRoute({ params }: RouteProps) {
  const { name } = await params;
  return <AgentRunsPage agentName={name} />;
}
