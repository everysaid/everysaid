import { api } from "./api";

// How each service looks (its name and colour: a dot or a badge beside a message), as the plugins
// that bring it declare (/api/services). Loaded once signed in, again when the language changes.
export interface ServiceLook {
  name: string;
  color: string;
  short: string;
  messages: boolean;     // false: a service of calls only
}

export let SERVICES: Record<string, ServiceLook> = {};

export async function loadServices(lang: string) {
  SERVICES = await api.get<Record<string, ServiceLook>>(`/api/services?lang=${lang}`);
  return SERVICES;
}

export function service(id: string): ServiceLook {
  return SERVICES[id] ?? { name: id, color: "#8e94a3", short: id.slice(0, 2), messages: true };
}
