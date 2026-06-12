import { RunDetailsPage } from "@/components/run-details-page";

type RouteProps = {
  params: Promise<{
    id: string;
  }>;
};

export default async function RunDetailsRoute({ params }: RouteProps) {
  const { id } = await params;
  return <RunDetailsPage runId={id} />;
}
