import { encode, sessionURL } from "./project-api";

export type ConversationSource = {
  kind: "owned" | "received" | "publicSnapshot";
  key: string;
  authorityRevision?: number;
  session: string;
  base: string;
  events?: string;
  capabilities: {
    read: boolean;
    run: boolean;
    edit: boolean;
    copy: boolean;
    download: boolean;
  };
};
export function ownedConversation(
  project: string,
  session: string,
): ConversationSource {
  return {
    kind: "owned",
    key: project,
    session,
    base: sessionURL(project, session),
    capabilities: {
      read: true,
      run: true,
      edit: true,
      copy: false,
      download: true,
    },
  };
}
export function receivedConversation(
  session: string,
  revision: number,
  download: boolean,
  copy = true,
): ConversationSource {
  const base = `/api/conversations/${encode(session)}`;
  return {
    kind: "received",
    authorityRevision: revision,
    key: `received:${session}:${revision}`,
    session,
    base,
    events: `${base}/view/events`,
    capabilities: { read: true, run: false, edit: false, copy, download },
  };
}
export type SharedPerson = {
  cursor?: string;
  id: string;
  username: string;
  display_name: string;
  shared_at: string;
};
export type ReceivedConversation = {
  cursor?: string;
  can_copy?: boolean;
  session_id: string;
  title: string;
  source: SharedPerson;
  shared_at: string;
  revision: number;
  allow_download: boolean;
  paths: string[];
};
