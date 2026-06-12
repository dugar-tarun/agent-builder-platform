import { AgentBuilderPage } from "@/components/agent-builder-page";

type RouteProps = {
  params: Promise<{
    name: string;
  }>;
};

export default async function AgentBuilderRoute({ params }: RouteProps) {
  const { name } = await params;
  return <AgentBuilderPage agentName={name} />;
}
