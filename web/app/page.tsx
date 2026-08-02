import { redirect } from "next/navigation";

// Корень ведёт в кабинет; /lessons сам отправит незалогиненных на /login.
export default function Home() {
  redirect("/lessons");
}
