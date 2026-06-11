import type { NextRequest } from "next/server";

const CONTROLPLANE_BASE_URL = process.env.CONTROLPLANE_BASE_URL ?? "http://localhost:8080";

async function proxy(request: NextRequest, path: string[]) {
  const url = new URL(request.url);
  const upstream = new URL(`/` + path.join("/"), CONTROLPLANE_BASE_URL);
  upstream.search = url.search;

  const headers = new Headers();
  for (const [key, value] of request.headers.entries()) {
    if (["host", "connection", "content-length"].includes(key.toLowerCase())) {
      continue;
    }
    headers.set(key, value);
  }

  const method = request.method.toUpperCase();
  const canHaveBody = !["GET", "HEAD"].includes(method);
  const body = canHaveBody ? await request.text() : undefined;

  const response = await fetch(upstream, {
    method,
    headers,
    body: canHaveBody ? body : undefined,
    redirect: "manual",
    cache: "no-store",
  });

  const outHeaders = new Headers();
  for (const [key, value] of response.headers.entries()) {
    if (["transfer-encoding", "connection", "content-encoding"].includes(key.toLowerCase())) {
      continue;
    }
    outHeaders.set(key, value);
  }

  const payload = await response.arrayBuffer();
  return new Response(payload, {
    status: response.status,
    statusText: response.statusText,
    headers: outHeaders,
  });
}

type RouteContext = {
  params: Promise<{
    path: string[];
  }>;
};

export async function GET(request: NextRequest, context: RouteContext) {
  const { path } = await context.params;
  return proxy(request, path);
}

export async function POST(request: NextRequest, context: RouteContext) {
  const { path } = await context.params;
  return proxy(request, path);
}

export async function PUT(request: NextRequest, context: RouteContext) {
  const { path } = await context.params;
  return proxy(request, path);
}

export async function PATCH(request: NextRequest, context: RouteContext) {
  const { path } = await context.params;
  return proxy(request, path);
}

export async function DELETE(request: NextRequest, context: RouteContext) {
  const { path } = await context.params;
  return proxy(request, path);
}
