export type Identity = {
  id: string;
  username: string;
  display_name: string;
  status: "active" | "disabled";
  role: "user" | "admin";
  revision: number;
  session_revision: number;
  must_change_password: boolean;
};
